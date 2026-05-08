package gateway_api

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"adminv.myds.me/a/gatewayworker-go/pkg/crypto"
	"adminv.myds.me/a/gatewayworker-go/pkg/protocol"
	"io"
	"net"
	"sync"
	"time"
)

// queryConnPool 独立的 Gateway 查询连接池
// 仿照 PHP Gateway::$gatewayConnections，与 BusinessWorker 的事件连接完全隔离
// 用于 GetSession、IsOnline 等需要 请求-响应 的方法
type queryConnPool struct {
	aesKey []byte
	mu     sync.Mutex
	conns  map[string]*pooledConn
	ttl    time.Duration
}

type pooledConn struct {
	conn      net.Conn
	createdAt time.Time
	mu        sync.Mutex // 序列化同一连接上的查询
}

var (
	pool     *queryConnPool
	poolOnce sync.Once
)

// initPool 初始化查询连接池
func initPool() {
	pool = &queryConnPool{
		aesKey: crypto.DeriveKey(bw.SecretKey),
		conns:  make(map[string]*pooledConn),
		ttl:    50 * time.Second, // 同 PHP
	}
}

// getConn 获取到指定 gateway 的查询连接（带 TTL 缓存）
func (p *queryConnPool) getConn(addr string) (*pooledConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	if pc, ok := p.conns[addr]; ok {
		if now.Sub(pc.createdAt) < p.ttl {
			return pc, nil
		}
		// TTL 过期，关闭旧连接
		pc.conn.Close()
		delete(p.conns, addr)
	}

	// 创建新连接
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to gateway %s failed: %w", addr, err)
	}

	// 发送认证包 (CmdGatewayClientConnect)
	authData := protocol.NewEmptyData()
	authData.Cmd = protocol.CmdGatewayClientConnect
	body, _ := json.Marshal(map[string]string{
		"secret_key": bw.SecretKey,
	})
	authData.Body = body
	authData.Flag = protocol.FlagBodyIsScalar
	if !p.sendEncrypted(conn, protocol.Encode(authData)) {
		conn.Close()
		return nil, fmt.Errorf("auth to gateway %s failed", addr)
	}

	pc := &pooledConn{conn: conn, createdAt: now}
	p.conns[addr] = pc
	return pc, nil
}

// removeConn 移除失效连接
func (p *queryConnPool) removeConn(addr string) {
	p.mu.Lock()
	if pc, ok := p.conns[addr]; ok {
		pc.conn.Close()
		delete(p.conns, addr)
	}
	p.mu.Unlock()
}

func (p *queryConnPool) sendEncrypted(conn net.Conn, data []byte) bool {
	encrypted, err := crypto.Encrypt(data, p.aesKey)
	if err != nil {
		return false
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	_, err = conn.Write(buf)
	return err == nil
}

// readEncryptedPacket 读取加密数据包
func (p *queryConnPool) readEncryptedPacket(conn net.Conn) ([]byte, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length > protocol.MaxEncryptedPacketSize {
		return nil, fmt.Errorf("packet too large: %d", length)
	}
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(conn, ciphertext); err != nil {
		return nil, err
	}
	return crypto.Decrypt(ciphertext, p.aesKey)
}

// sendAndRecv 发送查询并同步读取响应
// 对应 PHP Gateway::sendAndRecv
func (p *queryConnPool) sendAndRecv(addr string, gd *protocol.GatewayData) ([]byte, error) {
	pc, err := p.getConn(addr)
	if err != nil {
		return nil, err
	}

	// 序列化同一连接上的查询，避免并发读写
	pc.mu.Lock()
	defer pc.mu.Unlock()

	encoded := protocol.Encode(gd)
	if !p.sendEncrypted(pc.conn, encoded) {
		p.removeConn(addr)
		return nil, fmt.Errorf("send to gateway %s failed", addr)
	}

	// 设置读超时
	pc.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer pc.conn.SetReadDeadline(time.Time{})

	plaintext, err := p.readEncryptedPacket(pc.conn)
	if err != nil {
		p.removeConn(addr)
		return nil, fmt.Errorf("recv from gateway %s failed: %w", addr, err)
	}

	// 响应格式: [4B data_len][json_data]
	if len(plaintext) < 4 {
		return nil, fmt.Errorf("invalid response from %s: too short", addr)
	}
	dataLen := binary.BigEndian.Uint32(plaintext[0:4])
	if uint32(len(plaintext)) < 4+dataLen {
		return nil, fmt.Errorf("invalid response from %s: data truncated", addr)
	}
	return plaintext[4 : 4+dataLen], nil
}

// gatewayResult 单个 gateway 的查询结果
type gatewayResult struct {
	addr string
	data []byte
	err  error
}

// queryAllGateways 向所有 gateway 并发查询并收集结果
// 利用 Go 并发优势，比 PHP 的 stream_select 串行轮询更高效
func (p *queryConnPool) queryAllGateways(gd *protocol.GatewayData) []gatewayResult {
	addrs := getAllGatewayAddresses()
	if len(addrs) == 0 {
		return nil
	}

	results := make([]gatewayResult, len(addrs))
	var wg sync.WaitGroup

	for i, addr := range addrs {
		wg.Add(1)
		go func(idx int, a string) {
			defer wg.Done()
			data, err := p.sendAndRecv(a, gd)
			results[idx] = gatewayResult{addr: a, data: data, err: err}
		}(i, addr)
	}

	wg.Wait()
	return results
}

// querySingleGateway 向指定 gateway 查询
func (p *queryConnPool) querySingleGateway(addr string, gd *protocol.GatewayData) ([]byte, error) {
	return p.sendAndRecv(addr, gd)
}

// getAllGatewayAddresses 获取所有 gateway 地址
func getAllGatewayAddresses() []string {
	if bw == nil {
		return nil
	}
	return bw.GetAllGatewayAddresses()
}

// ensurePool 确保连接池已初始化（线程安全）
func ensurePool() {
	poolOnce.Do(initPool)
}
