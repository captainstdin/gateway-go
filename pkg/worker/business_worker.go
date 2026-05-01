package worker

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	gwctx "gatewayworker-go/pkg/context"
	"gatewayworker-go/pkg/crypto"
	"gatewayworker-go/pkg/protocol"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type BusinessWorker struct {
	RegisterAddr []string
	SecretKey    string
	Name         string
	ID           int

	OnWorkerStart      func()
	OnWorkerStop       func()
	OnConnect          func(clientID string)
	OnMessage          func(clientID string, message []byte)
	OnClose            func(clientID string)
	OnWebSocketConnect func(clientID string, data []byte)

	aesKey          []byte
	gatewayConns    map[string]net.Conn
	gatewayAddrs    map[string]string
	connectingAddrs map[string]bool
	processedCount  uint64 // atomic, 累计处理消息数
	mu              sync.RWMutex
	stopCh          chan struct{}
}

func New(name string, id int, registerAddr []string, secretKey string) *BusinessWorker {
	return &BusinessWorker{
		RegisterAddr:    registerAddr,
		SecretKey:       secretKey,
		Name:            name,
		ID:              id,
		aesKey:          crypto.DeriveKey(secretKey),
		gatewayConns:    make(map[string]net.Conn),
		gatewayAddrs:    make(map[string]string),
		connectingAddrs: make(map[string]bool),
		stopCh:          make(chan struct{}),
	}
}

func (bw *BusinessWorker) Run() error {
	if bw.OnWorkerStart != nil {
		bw.OnWorkerStart()
	}
	for _, regAddr := range bw.RegisterAddr {
		go bw.maintainRegisterConn(regAddr)
	}
	<-bw.stopCh
	if bw.OnWorkerStop != nil {
		bw.OnWorkerStop()
	}
	return nil
}

// GetProcessedCount 获取累计处理消息数
func (bw *BusinessWorker) GetProcessedCount() uint64 {
	return atomic.LoadUint64(&bw.processedCount)
}

// ResetStats 归零处理计数器
func (bw *BusinessWorker) ResetStats() {
	atomic.StoreUint64(&bw.processedCount, 0)
}

// WorkerName 返回 "name:id" 格式的名称
func (bw *BusinessWorker) WorkerName() string {
	return fmt.Sprintf("%s:%d", bw.Name, bw.ID)
}

func (bw *BusinessWorker) Stop() { close(bw.stopCh) }

func (bw *BusinessWorker) GetGatewayConnections() map[string]net.Conn {
	bw.mu.RLock()
	defer bw.mu.RUnlock()
	cp := make(map[string]net.Conn, len(bw.gatewayConns))
	for k, v := range bw.gatewayConns {
		cp[k] = v
	}
	return cp
}

func (bw *BusinessWorker) GetAllGatewayAddresses() []string {
	bw.mu.RLock()
	defer bw.mu.RUnlock()
	addrs := make([]string, 0, len(bw.gatewayAddrs))
	for addr := range bw.gatewayAddrs {
		addrs = append(addrs, addr)
	}
	return addrs
}

func (bw *BusinessWorker) maintainRegisterConn(regAddr string) {
	for {
		select {
		case <-bw.stopCh:
			return
		default:
		}
		conn, err := net.DialTimeout("tcp", regAddr, 3*time.Second)
		if err != nil {
			log.Printf("[Worker] Connect to register %s failed: %v", regAddr, err)
			time.Sleep(time.Second)
			continue
		}
		log.Printf("[Worker] Connected to register %s", regAddr)

		msg, _ := json.Marshal(map[string]string{
			"event": "worker_connect", "secret_key": bw.SecretKey, "name": bw.WorkerName(),
		})
		encrypted, _ := crypto.EncryptToBase64(msg, bw.aesKey)
		conn.Write([]byte(encrypted + "\n"))

		// Heartbeat + Stats reporting
		go func() {
			heartbeat := time.NewTicker(25 * time.Second)
			statsReport := time.NewTicker(3 * time.Second)
			defer heartbeat.Stop()
			defer statsReport.Stop()
			for {
				select {
				case <-bw.stopCh:
					return
				case <-heartbeat.C:
					ping, _ := json.Marshal(map[string]string{"event": "ping"})
					enc, _ := crypto.EncryptToBase64(ping, bw.aesKey)
					if _, err := conn.Write([]byte(enc + "\n")); err != nil {
						return
					}
				case <-statsReport.C:
					stats, _ := json.Marshal(map[string]interface{}{
						"event": "worker_stats",
						"name":  bw.WorkerName(),
						"processed_count": atomic.LoadUint64(&bw.processedCount),
					})
					enc, _ := crypto.EncryptToBase64(stats, bw.aesKey)
					if _, err := conn.Write([]byte(enc + "\n")); err != nil {
						return
					}
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
			plaintext, err := crypto.DecryptFromBase64(line, bw.aesKey)
			if err != nil {
				continue
			}
			var regMsg struct {
				Event     string   `json:"event"`
				Addresses []string `json:"addresses"`
			}
			if json.Unmarshal(plaintext, &regMsg) != nil {
				continue
			}
			if regMsg.Event == "broadcast_addresses" {
				bw.onBroadcastAddresses(regMsg.Addresses)
			}
		}
		conn.Close()
		log.Printf("[Worker] Register connection lost, reconnecting...")
		time.Sleep(time.Second)
	}
}

func (bw *BusinessWorker) onBroadcastAddresses(addresses []string) {
	bw.mu.Lock()
	bw.gatewayAddrs = make(map[string]string)
	for _, addr := range addresses {
		bw.gatewayAddrs[addr] = addr
	}
	bw.mu.Unlock()

	for _, addr := range addresses {
		bw.mu.RLock()
		_, connected := bw.gatewayConns[addr]
		_, connecting := bw.connectingAddrs[addr]
		bw.mu.RUnlock()
		if !connected && !connecting {
			go bw.connectToGateway(addr)
		}
	}
}

func (bw *BusinessWorker) connectToGateway(addr string) {
	bw.mu.Lock()
	bw.connectingAddrs[addr] = true
	bw.mu.Unlock()

	defer func() {
		bw.mu.Lock()
		delete(bw.connectingAddrs, addr)
		bw.mu.Unlock()
	}()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		log.Printf("[Worker] Connect to gateway %s failed: %v", addr, err)
		bw.scheduleReconnect(addr)
		return
	}

	// Send auth
	authData := protocol.NewEmptyData()
	authData.Cmd = protocol.CmdWorkerConnect
	body, _ := json.Marshal(map[string]string{
		"worker_key": fmt.Sprintf("%s:%d", bw.Name, bw.ID),
		"secret_key": bw.SecretKey,
	})
	authData.Body = body
	authData.Flag = protocol.FlagBodyIsScalar
	bw.sendEncrypted(conn, protocol.Encode(authData))

	bw.mu.Lock()
	bw.gatewayConns[addr] = conn
	bw.mu.Unlock()
	log.Printf("[Worker] Connected to gateway %s", addr)

	// Read loop
	for {
		plaintext, err := bw.readEncryptedPacket(conn)
		if err != nil {
			break
		}
		data, err := protocol.Decode(plaintext)
		if err != nil {
			continue
		}
		bw.onGatewayMessage(data, addr)
	}

	bw.mu.Lock()
	delete(bw.gatewayConns, addr)
	bw.mu.Unlock()
	conn.Close()
	log.Printf("[Worker] Gateway %s disconnected", addr)
	bw.scheduleReconnect(addr)
}

func (bw *BusinessWorker) scheduleReconnect(addr string) {
	time.AfterFunc(time.Second, func() {
		bw.mu.RLock()
		_, stillNeeded := bw.gatewayAddrs[addr]
		_, connected := bw.gatewayConns[addr]
		bw.mu.RUnlock()
		if stillNeeded && !connected {
			go bw.connectToGateway(addr)
		}
	})
}

func (bw *BusinessWorker) onGatewayMessage(data *protocol.GatewayData, gatewayAddr string) {
	if data.Cmd == protocol.CmdPing {
		return
	}
	clientID := gwctx.AddressToClientID(data.LocalIP, data.LocalPort, data.ConnectionID)

	switch data.Cmd {
	case protocol.CmdOnConnect:
		if bw.OnConnect != nil {
			bw.OnConnect(clientID)
		}
	case protocol.CmdOnMessage:
		atomic.AddUint64(&bw.processedCount, 1)
		if bw.OnMessage != nil {
			bw.OnMessage(clientID, data.Body)
		}
	case protocol.CmdOnClose:
		if bw.OnClose != nil {
			bw.OnClose(clientID)
		}
	case protocol.CmdOnWebSocketConnect:
		if bw.OnWebSocketConnect != nil {
			bw.OnWebSocketConnect(clientID, data.Body)
		}
	}
}

func (bw *BusinessWorker) sendEncrypted(conn net.Conn, data []byte) bool {
	encrypted, err := crypto.Encrypt(data, bw.aesKey)
	if err != nil {
		return false
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(encrypted)))
	_, err = conn.Write(append(lenBuf, encrypted...))
	return err == nil
}

func (bw *BusinessWorker) readEncryptedPacket(conn net.Conn) ([]byte, error) {
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
	return crypto.Decrypt(ciphertext, bw.aesKey)
}

// SendToGateway 向指定 gateway 发送已编码的数据（供 gateway_api 使用）
func (bw *BusinessWorker) SendToGateway(addr string, data []byte) bool {
	bw.mu.RLock()
	conn, ok := bw.gatewayConns[addr]
	bw.mu.RUnlock()
	if !ok {
		return false
	}
	return bw.sendEncrypted(conn, data)
}

// SendToAllGateways 向所有 gateway 发送（供 gateway_api 使用）
func (bw *BusinessWorker) SendToAllGateways(data []byte) {
	bw.mu.RLock()
	conns := make([]net.Conn, 0, len(bw.gatewayConns))
	for _, c := range bw.gatewayConns {
		conns = append(conns, c)
	}
	bw.mu.RUnlock()
	for _, c := range conns {
		bw.sendEncrypted(c, data)
	}
}
