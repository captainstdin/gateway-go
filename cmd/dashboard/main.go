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
	// Periodically query gateway client counts
	go d.pollGatewayStats()

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
				d.broadcast()
			}
		}
		conn.Close()
		log.Printf("[Dashboard] Register connection lost, reconnecting...")
		time.Sleep(time.Second)
	}
}

func (d *Dashboard) pollGatewayStats() {
	ticker := time.NewTicker(3 * time.Second)
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

const dashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>GatewayWorker-Go Dashboard</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0f1117;color:#e0e0e0;font-family:'Segoe UI',system-ui,sans-serif;min-height:100vh}
.header{background:linear-gradient(135deg,#1a1d2e,#252a3a);padding:20px 32px;border-bottom:1px solid #2d3348;display:flex;align-items:center;gap:16px}
.header h1{font-size:22px;font-weight:600;background:linear-gradient(135deg,#60a5fa,#a78bfa);-webkit-background-clip:text;-webkit-text-fill-color:transparent}
.header .dot{width:10px;height:10px;border-radius:50%;background:#22c55e;animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}
.header .time{margin-left:auto;color:#888;font-size:13px}
.stats{display:flex;gap:16px;padding:20px 32px}
.stat-card{flex:1;background:linear-gradient(135deg,#1e2235,#252a3e);border:1px solid #2d3348;border-radius:12px;padding:20px;text-align:center}
.stat-card .label{font-size:12px;color:#888;text-transform:uppercase;letter-spacing:1px}
.stat-card .value{font-size:36px;font-weight:700;margin-top:4px;background:linear-gradient(135deg,#60a5fa,#a78bfa);-webkit-background-clip:text;-webkit-text-fill-color:transparent}
.section{padding:12px 32px}
.section h2{font-size:15px;color:#888;margin-bottom:12px;text-transform:uppercase;letter-spacing:1px}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px}
.card{background:#1a1d2e;border:1px solid #2d3348;border-radius:10px;padding:16px;transition:border-color .2s,transform .2s}
.card:hover{border-color:#60a5fa;transform:translateY(-2px)}
.card .title{display:flex;align-items:center;gap:8px;margin-bottom:12px}
.card .title .indicator{width:8px;height:8px;border-radius:50%;background:#22c55e;flex-shrink:0}
.card .title span{font-weight:600;font-size:14px;color:#ccc;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.card .metric{display:flex;justify-content:space-between;align-items:baseline;padding:4px 0}
.card .metric .label{font-size:12px;color:#666}
.card .metric .val{font-size:20px;font-weight:700;color:#60a5fa}
.card .metric .val.purple{color:#a78bfa}
.empty{color:#555;font-size:14px;padding:20px;text-align:center}
.footer{text-align:center;padding:20px;color:#444;font-size:12px}
@keyframes fadeIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
.card{animation:fadeIn .3s ease}
</style>
</head>
<body>
<div class="header">
  <div class="dot" id="statusDot"></div>
  <h1>GatewayWorker-Go Dashboard</h1>
  <div class="time" id="updateTime">--:--:--</div>
</div>
<div class="stats">
  <div class="stat-card"><div class="label">在线连接</div><div class="value" id="totalClients">0</div></div>
  <div class="stat-card"><div class="label">Gateway 数</div><div class="value" id="totalGateways">0</div></div>
  <div class="stat-card"><div class="label">Worker 数</div><div class="value" id="totalWorkers">0</div></div>
</div>
<div class="section"><h2>Gateways</h2><div class="cards" id="gatewayCards"></div></div>
<div class="section"><h2>Business Workers</h2><div class="cards" id="workerCards"></div></div>
<div class="footer">GatewayWorker-Go Monitor · Real-time WebSocket</div>
<script>
const ws = new WebSocket((location.protocol==='https:'?'wss:':'ws:')+'//'+location.host+'/ws');
ws.onmessage = (e) => {
  const d = JSON.parse(e.data);
  document.getElementById('totalClients').textContent = d.total_clients;
  document.getElementById('totalGateways').textContent = d.gateways?d.gateways.length:0;
  document.getElementById('totalWorkers').textContent = d.total_workers;
  document.getElementById('updateTime').textContent = d.update_time;

  const gc = document.getElementById('gatewayCards');
  if(!d.gateways||d.gateways.length===0){gc.innerHTML='<div class="empty">暂无 Gateway</div>';}
  else{gc.innerHTML=d.gateways.map(g=>'<div class="card"><div class="title"><div class="indicator"></div><span>'+g.addr+'</span></div><div class="metric"><span class="label">在线连接</span><span class="val">'+g.client_count+'</span></div></div>').join('');}

  const wc = document.getElementById('workerCards');
  if(!d.workers||d.workers.length===0){wc.innerHTML='<div class="empty">暂无 Worker</div>';}
  else{wc.innerHTML=d.workers.map(w=>'<div class="card"><div class="title"><div class="indicator"></div><span>'+(w.name||w.addr)+'</span></div><div class="metric"><span class="label">处理次数</span><span class="val purple">'+w.processed_count.toLocaleString()+'</span></div><div class="metric"><span class="label">地址</span><span class="label">'+w.addr+'</span></div></div>').join('');}
};
ws.onclose = () => {document.getElementById('statusDot').style.background='#ef4444';};
</script>
</body>
</html>`

func main() {
	listen := flag.String("listen", "0.0.0.0:8686", "Dashboard web listen address")
	registerAddr := flag.String("register", "127.0.0.1:1236", "Register address(es), comma separated")
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
