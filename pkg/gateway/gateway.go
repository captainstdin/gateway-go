package gateway

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"gatewayworker-go/pkg/crypto"
	"gatewayworker-go/pkg/protocol"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// listenEntry 解析后的监听地址条目
type listenEntry struct {
	Protocol string // "websocket" 或 "tcp"
	Addr     string // "0.0.0.0:7272"
}

// parseListenAddr 解析 PHP 风格的监听地址
// 支持格式: "websocket://0.0.0.0:7272", "tcp://0.0.0.0:7273", "ws://0.0.0.0:7272"
// 不带协议前缀默认为 websocket
func parseListenAddr(addr string) listenEntry {
	if strings.HasPrefix(addr, "websocket://") {
		return listenEntry{Protocol: "websocket", Addr: strings.TrimPrefix(addr, "websocket://")}
	}
	if strings.HasPrefix(addr, "ws://") {
		return listenEntry{Protocol: "websocket", Addr: strings.TrimPrefix(addr, "ws://")}
	}
	if strings.HasPrefix(addr, "tcp://") {
		return listenEntry{Protocol: "tcp", Addr: strings.TrimPrefix(addr, "tcp://")}
	}
	// 默认 websocket
	return listenEntry{Protocol: "websocket", Addr: addr}
}

type ClientConnection struct {
	ID               uint32
	Conn             ClientConn
	Session          string
	UID              string
	Groups           map[string]bool
	GatewayHeader    *protocol.GatewayData
	PingNotRespCount int
	BoundWorkerKey   string
}

type Gateway struct {
	ListenAddrs  []string // 支持: "websocket://0.0.0.0:7272", "tcp://0.0.0.0:7273"
	LanIP        string
	LanPort      int
	StartPort    int
	InstanceID   int
	RegisterAddr []string
	SecretKey    string
	PingInterval int
	PingNotResponseLimit int
	PingData     string
	RouterMode   RouterMode

	aesKey          []byte
	router          *Router
	clientConns     map[uint32]*ClientConnection
	uidConns        map[string]map[uint32]*ClientConnection
	groupConns      map[string]map[uint32]*ClientConnection
	workerConns     map[string]net.Conn
	connIDCounter   uint32
	mu              sync.RWMutex
	gatewayPort     int
	startTime       time.Time
	stopCh          chan struct{}
	upgrader        websocket.Upgrader
}

func New(cfg *Config) *Gateway {
	g := &Gateway{
		ListenAddrs:  cfg.ListenAddrs,
		LanIP:        cfg.LanIP,
		LanPort:      cfg.StartPort + cfg.InstanceID,
		StartPort:    cfg.StartPort,
		InstanceID:   cfg.InstanceID,
		RegisterAddr: cfg.RegisterAddr,
		SecretKey:    cfg.SecretKey,
		PingInterval: cfg.PingInterval,
		PingNotResponseLimit: cfg.PingNotResponseLimit,
		PingData:     cfg.PingData,
		RouterMode:   cfg.RouterMode,
		aesKey:       crypto.DeriveKey(cfg.SecretKey),
		router:       NewRouter(cfg.RouterMode),
		clientConns:  make(map[uint32]*ClientConnection),
		uidConns:     make(map[string]map[uint32]*ClientConnection),
		groupConns:   make(map[string]map[uint32]*ClientConnection),
		workerConns:  make(map[string]net.Conn),
		stopCh:       make(chan struct{}),
		upgrader:     websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
	if g.LanIP == "" {
		g.LanIP = "127.0.0.1"
	}
	if g.StartPort == 0 {
		g.StartPort = 54321
		g.LanPort = g.StartPort + g.InstanceID
	}
	return g
}

type Config struct {
	ListenAddrs          []string // 如 ["websocket://0.0.0.0:7272", "tcp://0.0.0.0:7273"]
	LanIP                string
	StartPort            int
	InstanceID           int
	RegisterAddr         []string
	SecretKey            string
	PingInterval         int
	PingNotResponseLimit int
	PingData             string
	RouterMode           RouterMode
}

func (g *Gateway) Run() error {
	g.startTime = time.Now()

	// 启动内部通讯端口（供 Worker/GatewayClient 连接）
	innerAddr := fmt.Sprintf("%s:%d", g.LanIP, g.LanPort)
	innerLn, err := net.Listen("tcp", innerAddr)
	if err != nil {
		return fmt.Errorf("inner listen failed: %w", err)
	}
	log.Printf("[Gateway] Inner listening on %s", innerAddr)
	go g.acceptWorkerConns(innerLn)
	go g.registerToCenter()

	if g.PingInterval > 0 {
		go g.pingLoop()
	}

	// 解析并启动所有对外监听地址
	for _, raw := range g.ListenAddrs {
		entry := parseListenAddr(raw)
		switch entry.Protocol {
		case "tcp":
			tcpLn, err := net.Listen("tcp", entry.Addr)
			if err != nil {
				return fmt.Errorf("tcp listen on %s failed: %w", entry.Addr, err)
			}
			log.Printf("[Gateway] TCP listening on tcp://%s", entry.Addr)
			go g.acceptTCPClients(tcpLn)

		case "websocket":
			log.Printf("[Gateway] WebSocket listening on websocket://%s", entry.Addr)
			mux := http.NewServeMux()
			mux.HandleFunc("/", g.handleWebSocket)
			server := &http.Server{Addr: entry.Addr, Handler: mux}
			go func(s *http.Server) {
				if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Printf("[Gateway] WebSocket server error: %v", err)
				}
			}(server)

		default:
			log.Printf("[Gateway] Unknown protocol: %s in %s", entry.Protocol, raw)
		}
	}

	<-g.stopCh
	return nil
}

func (g *Gateway) Stop() { close(g.stopCh) }

func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	wsConn, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	cc := NewWSClientConn(wsConn)
	clientConn := g.onClientConnect(cc)
	// Send websocket connect event with headers
	serverData := map[string]interface{}{
		"get":    r.URL.Query(),
		"server": map[string]string{"REQUEST_URI": r.RequestURI, "HTTP_HOST": r.Host},
		"cookie": map[string]string{},
	}
	body, _ := json.Marshal(serverData)
	g.sendToWorker(protocol.CmdOnWebSocketConnect, clientConn, body)

	for {
		data, err := cc.Read()
		if err != nil {
			break
		}
		g.onClientMessage(clientConn, data)
	}
	g.onClientClose(clientConn)
}

func (g *Gateway) acceptTCPClients(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-g.stopCh:
				return
			default:
				continue
			}
		}
		go func(c net.Conn) {
			cc := NewTCPClientConn(c)
			clientConn := g.onClientConnect(cc)
			g.sendToWorker(protocol.CmdOnConnect, clientConn, nil)
			for {
				data, err := cc.Read()
				if err != nil {
					break
				}
				g.onClientMessage(clientConn, data)
			}
			g.onClientClose(clientConn)
		}(conn)
	}
}

func (g *Gateway) generateConnectionID() uint32 {
	for {
		id := atomic.AddUint32(&g.connIDCounter, 1)
		if id == 0 {
			continue
		}
		g.mu.RLock()
		_, exists := g.clientConns[id]
		g.mu.RUnlock()
		if !exists {
			return id
		}
	}
}

func (g *Gateway) onClientConnect(cc ClientConn) *ClientConnection {
	connID := g.generateConnectionID()
	clientIP := net.ParseIP(cc.RemoteIP())
	var ipNum uint32
	if ip4 := clientIP.To4(); ip4 != nil {
		ipNum = binary.BigEndian.Uint32(ip4)
	}

	conn := &ClientConnection{
		ID:     connID,
		Conn:   cc,
		Groups: make(map[string]bool),
		GatewayHeader: &protocol.GatewayData{
			LocalIP:      ipToUint32(g.LanIP),
			LocalPort:    uint16(g.LanPort),
			ClientIP:     ipNum,
			ClientPort:   uint16(cc.RemotePort()),
			GatewayPort:  uint16(g.gatewayPort),
			ConnectionID: connID,
		},
		PingNotRespCount: -1,
	}

	g.mu.Lock()
	g.clientConns[connID] = conn
	g.mu.Unlock()
	log.Printf("[Gateway] Client connected: %s connID=%d", cc.RemoteAddr(), connID)
	return conn
}

func (g *Gateway) onClientMessage(conn *ClientConnection, data []byte) {
	conn.PingNotRespCount = -1
	g.sendToWorker(protocol.CmdOnMessage, conn, data)
}

func (g *Gateway) onClientClose(conn *ClientConnection) {
	g.sendToWorker(protocol.CmdOnClose, conn, nil)
	g.mu.Lock()
	delete(g.clientConns, conn.ID)
	if conn.UID != "" {
		if uidMap, ok := g.uidConns[conn.UID]; ok {
			delete(uidMap, conn.ID)
			if len(uidMap) == 0 {
				delete(g.uidConns, conn.UID)
			}
		}
	}
	for group := range conn.Groups {
		if gm, ok := g.groupConns[group]; ok {
			delete(gm, conn.ID)
			if len(gm) == 0 {
				delete(g.groupConns, group)
			}
		}
	}
	if conn.BoundWorkerKey != "" {
		g.router.DecrementCount(conn.BoundWorkerKey)
	}
	g.mu.Unlock()
	conn.Conn.Close()
	log.Printf("[Gateway] Client disconnected: connID=%d", conn.ID)
}

func (g *Gateway) sendToWorker(cmd uint8, conn *ClientConnection, body []byte) bool {
	g.mu.RLock()
	workerKeys := make([]string, 0, len(g.workerConns))
	for k := range g.workerConns {
		workerKeys = append(workerKeys, k)
	}
	g.mu.RUnlock()

	if len(workerKeys) == 0 {
		if time.Since(g.startTime) > 2*time.Second {
			log.Printf("[Gateway] No available workers")
		}
		return false
	}

	selectedKey := g.router.SelectWorker(workerKeys, conn.BoundWorkerKey)
	if conn.BoundWorkerKey == "" {
		conn.BoundWorkerKey = selectedKey
		g.router.IncrementCount(selectedKey)
	}

	gd := *conn.GatewayHeader
	gd.Cmd = cmd
	gd.Body = body
	gd.ExtData = conn.Session
	gd.Flag = protocol.FlagBodyIsScalar

	encoded := protocol.Encode(&gd)
	return g.sendEncrypted(g.workerConns[selectedKey], encoded)
}

func (g *Gateway) sendEncrypted(conn net.Conn, data []byte) bool {
	if conn == nil {
		return false
	}
	encrypted, err := crypto.Encrypt(data, g.aesKey)
	if err != nil {
		log.Printf("[Gateway] Encrypt error: %v", err)
		return false
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(encrypted)))
	_, err = conn.Write(append(lenBuf, encrypted...))
	return err == nil
}

func (g *Gateway) readEncryptedPacket(conn net.Conn) ([]byte, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length > 50*1024*1024 {
		return nil, fmt.Errorf("packet too large: %d", length)
	}
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(conn, ciphertext); err != nil {
		return nil, err
	}
	return crypto.Decrypt(ciphertext, g.aesKey)
}

// ----- Inner TCP: Worker connections -----

func (g *Gateway) acceptWorkerConns(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-g.stopCh:
				return
			default:
				continue
			}
		}
		go g.handleWorkerConn(conn)
	}
}

func (g *Gateway) handleWorkerConn(conn net.Conn) {
	defer conn.Close()
	authorized := false
	workerKey := ""

	for {
		plaintext, err := g.readEncryptedPacket(conn)
		if err != nil {
			break
		}
		data, err := protocol.Decode(plaintext)
		if err != nil {
			log.Printf("[Gateway] Decode error: %v", err)
			break
		}
		if !authorized && data.Cmd != protocol.CmdWorkerConnect && data.Cmd != protocol.CmdGatewayClientConnect {
			log.Printf("[Gateway] Unauthorized request from %s", conn.RemoteAddr())
			return
		}
		workerKey = g.handleWorkerMessage(conn, data, &authorized, workerKey)
		if workerKey == "__close__" {
			return
		}
	}

	if workerKey != "" && workerKey != "__close__" {
		g.mu.Lock()
		delete(g.workerConns, workerKey)
		g.mu.Unlock()
		g.router.OnWorkerDisconnected(workerKey)
		log.Printf("[Gateway] Worker disconnected: %s", workerKey)
	}
}

// ----- Register connection -----

func (g *Gateway) registerToCenter() {
	address := fmt.Sprintf("%s:%d", g.LanIP, g.LanPort)
	for _, regAddr := range g.RegisterAddr {
		go g.maintainRegisterConn(regAddr, address)
	}
}

func (g *Gateway) maintainRegisterConn(regAddr, selfAddr string) {
	for {
		select {
		case <-g.stopCh:
			return
		default:
		}
		conn, err := net.DialTimeout("tcp", regAddr, 3*time.Second)
		if err != nil {
			log.Printf("[Gateway] Connect to register %s failed: %v", regAddr, err)
			time.Sleep(time.Second)
			continue
		}
		msg, _ := json.Marshal(map[string]string{
			"event": "gateway_connect", "address": selfAddr, "secret_key": g.SecretKey,
		})
		encrypted, _ := crypto.EncryptToBase64(msg, g.aesKey)
		conn.Write([]byte(encrypted + "\n"))

		// Keep alive
		go func() {
			ticker := time.NewTicker(25 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-g.stopCh:
					return
				case <-ticker.C:
					ping, _ := json.Marshal(map[string]string{"event": "ping"})
					enc, _ := crypto.EncryptToBase64(ping, g.aesKey)
					if _, err := conn.Write([]byte(enc + "\n")); err != nil {
						return
					}
				}
			}
		}()

		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			// Register may send messages but gateway doesn't need them
		}
		conn.Close()
		log.Printf("[Gateway] Register connection lost, reconnecting...")
		time.Sleep(time.Second)
	}
}

// ----- Ping -----

func (g *Gateway) pingLoop() {
	interval := time.Duration(g.PingInterval) * time.Second
	if g.PingNotResponseLimit > 0 {
		interval = interval / 2
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.ping()
		}
	}
}

func (g *Gateway) ping() {
	g.mu.RLock()
	conns := make([]*ClientConnection, 0, len(g.clientConns))
	for _, c := range g.clientConns {
		conns = append(conns, c)
	}
	g.mu.RUnlock()

	for _, conn := range conns {
		if g.PingNotResponseLimit > 0 && conn.PingNotRespCount >= g.PingNotResponseLimit*2 {
			conn.Conn.Close()
			continue
		}
		conn.PingNotRespCount++
		if g.PingData != "" {
			if conn.PingNotRespCount == 0 {
				continue
			}
			if g.PingNotResponseLimit > 0 && conn.PingNotRespCount%2 == 1 {
				continue
			}
			conn.Conn.Write([]byte(g.PingData))
		}
	}

	// Ping workers
	g.mu.RLock()
	for _, wconn := range g.workerConns {
		gd := protocol.NewEmptyData()
		gd.Cmd = protocol.CmdPing
		g.sendEncrypted(wconn, protocol.Encode(gd))
	}
	g.mu.RUnlock()
}

func ipToUint32(ip string) uint32 {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 0
	}
	ip4 := parsed.To4()
	if ip4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip4)
}

func uint32ToIP(n uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", n>>24, (n>>16)&0xff, (n>>8)&0xff, n&0xff)
}
