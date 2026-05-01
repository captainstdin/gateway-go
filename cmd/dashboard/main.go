package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"gatewayworker-go/pkg/crypto"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "embed"

	"github.com/gorilla/websocket"
)

type WorkerInfo struct {
	Name           string `json:"name"`
	Addr           string `json:"addr"`
	ProcessedCount uint64 `json:"processed_count"`
}

type GatewayInfo struct {
	Addr        string `json:"addr"`
	ClientCount int    `json:"client_count"`
}

type DashboardStatus struct {
	Gateways     []GatewayInfo `json:"gateways"`
	Workers      []WorkerInfo  `json:"workers"`
	TotalClients int           `json:"total_clients"`
	TotalWorkers int           `json:"total_workers"`
	UpdateTime   string        `json:"update_time"`
}

type Dashboard struct {
	registerAddr []string
	secretKey    string
	listenAddr   string
	aesKey       []byte

	mu             sync.RWMutex
	gatewayAddrs   []string
	workers        []WorkerInfo
	gatewayStats   map[string]int
	wsClients      map[*websocket.Conn]bool
	wsMu           sync.Mutex
	upgrader       websocket.Upgrader
}

func newDashboard(registerAddr []string, secretKey, listenAddr string) *Dashboard {
	return &Dashboard{
		registerAddr: registerAddr,
		secretKey:    secretKey,
		listenAddr:   listenAddr,
		aesKey:       crypto.DeriveKey(secretKey),
		gatewayStats: make(map[string]int),
		wsClients:    make(map[*websocket.Conn]bool),
		upgrader:     websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
}

func (d *Dashboard) run() {
	// Connect to register as admin
	for _, addr := range d.registerAddr {
		go d.connectRegister(addr)
	}
	// Periodically poll gateway for client counts & worker stats
	go d.pollStats()

	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleIndex)
	mux.HandleFunc("/ws", d.handleWS)

	log.Printf("[Dashboard] Web UI at http://%s", d.listenAddr)
	if err := http.ListenAndServe(d.listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func (d *Dashboard) connectRegister(regAddr string) {
	for {
		conn, err := net.DialTimeout("tcp", regAddr, 3*time.Second)
		if err != nil {
			log.Printf("[Dashboard] Connect to register %s failed: %v", regAddr, err)
			time.Sleep(time.Second)
			continue
		}
		log.Printf("[Dashboard] Connected to register %s", regAddr)

		msg, _ := json.Marshal(map[string]string{
			"event": "admin_connect", "secret_key": d.secretKey,
		})
		enc, _ := crypto.EncryptToBase64(msg, d.aesKey)
		conn.Write([]byte(enc + "\n"))

		// Heartbeat
		go func() {
			ticker := time.NewTicker(25 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				ping, _ := json.Marshal(map[string]string{"event": "ping"})
				e, _ := crypto.EncryptToBase64(ping, d.aesKey)
				if _, err := conn.Write([]byte(e + "\n")); err != nil {
					return
				}
			}
		}()

		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			plaintext, err := crypto.DecryptFromBase64(line, d.aesKey)
			if err != nil {
				continue
			}
			var msg struct {
				Event    string       `json:"event"`
				Gateways []string     `json:"gateways"`
				Workers  []WorkerInfo `json:"workers"`
			}
			if json.Unmarshal(plaintext, &msg) != nil {
				continue
			}
			if msg.Event == "status_update" {
				d.mu.Lock()
				d.gatewayAddrs = msg.Gateways
				d.workers = msg.Workers
				d.mu.Unlock()
			}
		}
		conn.Close()
		log.Printf("[Dashboard] Register connection lost, reconnecting...")
		time.Sleep(time.Second)
	}
}

func (d *Dashboard) pollStats() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		d.mu.RLock()
		addrs := make([]string, len(d.gatewayAddrs))
		copy(addrs, d.gatewayAddrs)
		d.mu.RUnlock()

		stats := make(map[string]int)
		for _, addr := range addrs {
			count := d.queryGatewayCount(addr)
			stats[addr] = count
		}

		d.mu.Lock()
		d.gatewayStats = stats
		d.mu.Unlock()
		d.broadcast()
	}
}

func (d *Dashboard) queryGatewayCount(addr string) int {
	// Use a short-lived connection to query client count
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return -1
	}
	defer conn.Close()

	// Auth
	authBody, _ := json.Marshal(map[string]string{"secret_key": d.secretKey})
	authPkt := makeProtocolPacket(202, authBody, "") // CmdGatewayClientConnect
	d.sendEncrypted(conn, authPkt)

	// Query: CmdGetClientCountByGroup with empty group = total
	queryPkt := makeProtocolPacket(24, nil, "") // CmdGetClientCountByGroup
	d.sendEncrypted(conn, queryPkt)

	// Read response
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp := d.readEncryptedResponse(conn)
	if resp == nil {
		return -1
	}

	var count int
	// Response: 4-byte len + data
	if len(resp) >= 4 {
		dataLen := int(resp[0])<<24 | int(resp[1])<<16 | int(resp[2])<<8 | int(resp[3])
		if dataLen+4 <= len(resp) {
			json.Unmarshal(resp[4:4+dataLen], &count)
			return count
		}
	}
	json.Unmarshal(resp, &count)
	return count
}

func makeProtocolPacket(cmd uint8, body []byte, extData string) []byte {
	ext := []byte(extData)
	extLen := uint32(len(ext))
	bodyLen := uint32(len(body))
	packLen := uint32(28) + extLen + bodyLen
	buf := make([]byte, packLen)
	buf[0] = byte(packLen >> 24)
	buf[1] = byte(packLen >> 16)
	buf[2] = byte(packLen >> 8)
	buf[3] = byte(packLen)
	buf[4] = cmd
	buf[21] = 0x01 // FlagBodyIsScalar
	buf[24] = byte(extLen >> 24)
	buf[25] = byte(extLen >> 16)
	buf[26] = byte(extLen >> 8)
	buf[27] = byte(extLen)
	if extLen > 0 {
		copy(buf[28:], ext)
	}
	if bodyLen > 0 {
		copy(buf[28+extLen:], body)
	}
	return buf
}

func (d *Dashboard) sendEncrypted(conn net.Conn, data []byte) {
	encrypted, err := crypto.Encrypt(data, d.aesKey)
	if err != nil {
		return
	}
	lenBuf := []byte{byte(len(encrypted) >> 24), byte(len(encrypted) >> 16), byte(len(encrypted) >> 8), byte(len(encrypted))}
	conn.Write(append(lenBuf, encrypted...))
}

func (d *Dashboard) readEncryptedResponse(conn net.Conn) []byte {
	lenBuf := make([]byte, 4)
	if _, err := bufio.NewReader(conn).Read(lenBuf); err != nil {
		return nil
	}
	length := int(lenBuf[0])<<24 | int(lenBuf[1])<<16 | int(lenBuf[2])<<8 | int(lenBuf[3])
	if length <= 0 || length > 10*1024*1024 {
		return nil
	}
	buf := make([]byte, length)
	n := 0
	for n < length {
		nn, err := conn.Read(buf[n:])
		if err != nil {
			return nil
		}
		n += nn
	}
	plaintext, err := crypto.Decrypt(buf, d.aesKey)
	if err != nil {
		return nil
	}
	return plaintext
}

func (d *Dashboard) getStatus() DashboardStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()

	gateways := make([]GatewayInfo, 0, len(d.gatewayAddrs))
	totalClients := 0
	for _, addr := range d.gatewayAddrs {
		count := d.gatewayStats[addr]
		if count < 0 {
			count = 0
		}
		gateways = append(gateways, GatewayInfo{Addr: addr, ClientCount: count})
		totalClients += count
	}

	workers := make([]WorkerInfo, len(d.workers))
	copy(workers, d.workers)

	return DashboardStatus{
		Gateways:     gateways,
		Workers:      workers,
		TotalClients: totalClients,
		TotalWorkers: len(workers),
		UpdateTime:   time.Now().Format("15:04:05"),
	}
}

func (d *Dashboard) broadcast() {
	status := d.getStatus()
	data, _ := json.Marshal(status)

	d.wsMu.Lock()
	defer d.wsMu.Unlock()
	for ws := range d.wsClients {
		if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
			ws.Close()
			delete(d.wsClients, ws)
		}
	}
}

func (d *Dashboard) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	d.wsMu.Lock()
	d.wsClients[ws] = true
	d.wsMu.Unlock()

	// Send initial status
	status := d.getStatus()
	data, _ := json.Marshal(status)
	ws.WriteMessage(websocket.TextMessage, data)

	// Keep alive read loop
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			break
		}
	}
	d.wsMu.Lock()
	delete(d.wsClients, ws)
	d.wsMu.Unlock()
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

//go:embed template.html
var dashboardHTML string

func main() {
	listen := flag.String("listen", "0.0.0.0:8686", "Dashboard web listen address")
	registerAddr := flag.String("register", "127.0.0.1:51234", "Register address(es), comma separated")
	secretKey := flag.String("key", "", "Secret key")
	flag.Parse()

	d := newDashboard(strings.Split(*registerAddr, ","), *secretKey, *listen)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		os.Exit(0)
	}()

	d.run()
}
