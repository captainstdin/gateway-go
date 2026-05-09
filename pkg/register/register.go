package register

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/captainstdin/gateway-go/pkg/crypto"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Register 注册中心服务
type Register struct {
	ListenAddr string
	SecretKey  string

	aesKey             []byte
	listener           net.Listener
	gatewayConnections sync.Map // connID(int64) -> gateway内部地址(string)
	workerConnections  sync.Map // connID(int64) -> net.Conn
	adminConnections   sync.Map // connID(int64) -> net.Conn
	workerInfos        sync.Map // connID(int64) -> *WorkerInfo
	connIDCounter      int64
	stopCh             chan struct{}
}

// WorkerInfo Worker 上报的信息
type WorkerInfo struct {
	Name           string `json:"name"`
	Addr           string `json:"addr"`
	ProcessedCount uint64 `json:"processed_count"`
}

// registerMessage 注册中心收发的 JSON 消息结构
type registerMessage struct {
	Event          string   `json:"event"`
	Address        string   `json:"address,omitempty"`
	SecretKey      string   `json:"secret_key,omitempty"`
	Addresses      []string `json:"addresses,omitempty"`
	Name           string   `json:"name,omitempty"`
	ProcessedCount uint64   `json:"processed_count,omitempty"`
	// admin 广播用
	Gateways []string      `json:"gateways,omitempty"`
	Workers  []*WorkerInfo `json:"workers,omitempty"`
}

func New(listenAddr, secretKey string) *Register {
	return &Register{
		ListenAddr: listenAddr,
		SecretKey:  secretKey,
		aesKey:     crypto.DeriveKey(secretKey),
		stopCh:     make(chan struct{}),
	}
}

func (r *Register) Run() error {
	var err error
	r.listener, err = net.Listen("tcp", r.ListenAddr)
	if err != nil {
		return fmt.Errorf("register listen failed: %w", err)
	}
	log.Printf("[Register] Listening on %s", r.ListenAddr)

	for {
		conn, err := r.listener.Accept()
		if err != nil {
			select {
			case <-r.stopCh:
				return nil
			default:
				log.Printf("[Register] Accept error: %v", err)
				continue
			}
		}
		go r.handleConnection(conn)
	}
}

func (r *Register) Stop() {
	close(r.stopCh)
	if r.listener != nil {
		r.listener.Close()
	}
}

func (r *Register) handleConnection(conn net.Conn) {
	connID := atomic.AddInt64(&r.connIDCounter, 1)
	connType := "" // "gateway", "worker", "admin"
	defer func() {
		conn.Close()
		r.onClose(connID, connType)
	}()

	authTimer := time.AfterFunc(10*time.Second, func() {
		log.Printf("[Register] Auth timeout from %s", conn.RemoteAddr())
		conn.Close()
	})
	authenticated := false

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		plaintext, err := crypto.DecryptFromBase64(line, r.aesKey)
		if err != nil {
			log.Printf("[Register] Decrypt error from %s: %v", conn.RemoteAddr(), err)
			return
		}

		var msg registerMessage
		if err := json.Unmarshal(plaintext, &msg); err != nil {
			log.Printf("[Register] JSON parse error from %s: %v", conn.RemoteAddr(), err)
			return
		}

		if msg.Event == "" {
			return
		}

		if !authenticated {
			authTimer.Stop()
		}

		switch msg.Event {
		case "gateway_connect":
			if msg.Address == "" || msg.SecretKey != r.SecretKey {
				return
			}
			authenticated = true
			connType = "gateway"
			r.gatewayConnections.Store(connID, msg.Address)
			log.Printf("[Register] Gateway registered: %s (connID=%d)", msg.Address, connID)
			r.broadcastAddresses(nil)
			r.broadcastStatusToAdmins()

		case "worker_connect":
			if msg.SecretKey != r.SecretKey {
				return
			}
			authenticated = true
			connType = "worker"
			r.workerConnections.Store(connID, conn)
			r.workerInfos.Store(connID, &WorkerInfo{
				Name: msg.Name,
				Addr: conn.RemoteAddr().String(),
			})
			log.Printf("[Register] Worker registered: %s from %s (connID=%d)", msg.Name, conn.RemoteAddr(), connID)
			r.broadcastAddresses(conn)
			r.broadcastStatusToAdmins()

		case "worker_stats":
			if !authenticated {
				return
			}
			if v, ok := r.workerInfos.Load(connID); ok {
				info := v.(*WorkerInfo)
				info.ProcessedCount = msg.ProcessedCount
				r.workerInfos.Store(connID, info)
			}

		case "admin_connect":
			if msg.SecretKey != r.SecretKey {
				return
			}
			authenticated = true
			connType = "admin"
			r.adminConnections.Store(connID, conn)
			log.Printf("[Register] Admin connected from %s (connID=%d)", conn.RemoteAddr(), connID)
			r.sendStatusToConn(conn)

		case "ping":
			// heartbeat

		default:
			log.Printf("[Register] Unknown event: %s", msg.Event)
		}
	}
}

func (r *Register) onClose(connID int64, connType string) {
	switch connType {
	case "gateway":
		r.gatewayConnections.Delete(connID)
		log.Printf("[Register] Gateway disconnected (connID=%d)", connID)
		r.broadcastAddresses(nil)
		r.broadcastStatusToAdmins()
	case "worker":
		r.workerConnections.Delete(connID)
		r.workerInfos.Delete(connID)
		log.Printf("[Register] Worker disconnected (connID=%d)", connID)
		r.broadcastStatusToAdmins()
	case "admin":
		r.adminConnections.Delete(connID)
		log.Printf("[Register] Admin disconnected (connID=%d)", connID)
	}
}

func (r *Register) broadcastAddresses(targetConn net.Conn) {
	addresses := r.collectGatewayAddresses()
	msg := registerMessage{Event: "broadcast_addresses", Addresses: addresses}
	line := r.encryptMessage(msg)
	if line == "" {
		return
	}
	if targetConn != nil {
		targetConn.Write([]byte(line))
		return
	}
	r.workerConnections.Range(func(_, value interface{}) bool {
		if conn, ok := value.(net.Conn); ok {
			conn.Write([]byte(line))
		}
		return true
	})
}

func (r *Register) broadcastStatusToAdmins() {
	gateways := r.collectGatewayAddresses()
	workers := r.collectWorkerInfos()
	msg := registerMessage{
		Event:    "status_update",
		Gateways: gateways,
		Workers:  workers,
	}
	line := r.encryptMessage(msg)
	if line == "" {
		return
	}
	r.adminConnections.Range(func(_, value interface{}) bool {
		if conn, ok := value.(net.Conn); ok {
			conn.Write([]byte(line))
		}
		return true
	})
}

func (r *Register) sendStatusToConn(conn net.Conn) {
	gateways := r.collectGatewayAddresses()
	workers := r.collectWorkerInfos()
	msg := registerMessage{
		Event:    "status_update",
		Gateways: gateways,
		Workers:  workers,
	}
	line := r.encryptMessage(msg)
	if line != "" {
		conn.Write([]byte(line))
	}
}

func (r *Register) collectGatewayAddresses() []string {
	addrSet := make(map[string]bool)
	r.gatewayConnections.Range(func(_, value interface{}) bool {
		addrSet[value.(string)] = true
		return true
	})
	addrs := make([]string, 0, len(addrSet))
	for a := range addrSet {
		addrs = append(addrs, a)
	}
	return addrs
}

func (r *Register) collectWorkerInfos() []*WorkerInfo {
	var workers []*WorkerInfo
	r.workerInfos.Range(func(_, value interface{}) bool {
		workers = append(workers, value.(*WorkerInfo))
		return true
	})
	return workers
}

func (r *Register) encryptMessage(msg registerMessage) string {
	data, err := json.Marshal(msg)
	if err != nil {
		return ""
	}
	encrypted, err := crypto.EncryptToBase64(data, r.aesKey)
	if err != nil {
		return ""
	}
	return encrypted + "\n"
}
