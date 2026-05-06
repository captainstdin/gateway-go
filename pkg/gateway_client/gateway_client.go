package gateway_client

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	gwctx "gatewayworker-go/pkg/context"
	"gatewayworker-go/pkg/crypto"
	"gatewayworker-go/pkg/protocol"
	"io"
	"net"
	"sync"
	"time"
)

type GatewayClient struct {
	RegisterAddr []string
	SecretKey    string
	ConnTimeout  time.Duration

	aesKey        []byte
	connPool      map[string]*poolEntry
	mu            sync.Mutex
	addrCache     []string
	addrCacheTime time.Time
}

type poolEntry struct {
	conn      net.Conn
	createdAt time.Time
}

func New(registerAddr []string, secretKey string) *GatewayClient {
	return &GatewayClient{
		RegisterAddr: registerAddr,
		SecretKey:    secretKey,
		ConnTimeout:  3 * time.Second,
		aesKey:       crypto.DeriveKey(secretKey),
		connPool:     make(map[string]*poolEntry),
	}
}

func (c *GatewayClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.connPool {
		e.conn.Close()
	}
	c.connPool = make(map[string]*poolEntry)
}

func (c *GatewayClient) getGatewayAddresses() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.addrCacheTime) < time.Second && len(c.addrCache) > 0 {
		return c.addrCache, nil
	}
	for _, regAddr := range c.RegisterAddr {
		conn, err := net.DialTimeout("tcp", regAddr, c.ConnTimeout)
		if err != nil {
			continue
		}
		msg, _ := json.Marshal(map[string]string{
			"event": "worker_connect", "secret_key": c.SecretKey,
		})
		enc, _ := crypto.EncryptToBase64(msg, c.aesKey)
		conn.Write([]byte(enc + "\n"))
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			line := scanner.Text()
			plaintext, err := crypto.DecryptFromBase64(line, c.aesKey)
			if err == nil {
				var resp struct {
					Addresses []string `json:"addresses"`
				}
				if json.Unmarshal(plaintext, &resp) == nil && len(resp.Addresses) > 0 {
					c.addrCache = resp.Addresses
					c.addrCacheTime = time.Now()
					conn.Close()
					return c.addrCache, nil
				}
			}
		}
		conn.Close()
	}
	if len(c.addrCache) > 0 {
		return c.addrCache, nil
	}
	return nil, fmt.Errorf("no gateway addresses available")
}

func (c *GatewayClient) getConn(addr string) (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.connPool[addr]; ok {
		if time.Since(e.createdAt) < 50*time.Second {
			return e.conn, nil
		}
		e.conn.Close()
		delete(c.connPool, addr)
	}

	conn, err := net.DialTimeout("tcp", addr, c.ConnTimeout)
	if err != nil {
		return nil, err
	}
	// Authenticate
	authData := protocol.NewEmptyData()
	authData.Cmd = protocol.CmdGatewayClientConnect
	body, _ := json.Marshal(map[string]string{"secret_key": c.SecretKey})
	authData.Body = body
	authData.Flag = protocol.FlagBodyIsScalar

	encrypted, err := crypto.Encrypt(protocol.Encode(authData), c.aesKey)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("encrypt auth failed: %w", err)
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	conn.Write(buf)

	c.connPool[addr] = &poolEntry{conn: conn, createdAt: time.Now()}
	return conn, nil
}

func (c *GatewayClient) sendToGateway(addr string, gd *protocol.GatewayData) error {
	conn, err := c.getConn(addr)
	if err != nil {
		return err
	}
	data := protocol.Encode(gd)
	encrypted, err := crypto.Encrypt(data, c.aesKey)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	_, err = conn.Write(buf)
	if err != nil {
		c.mu.Lock()
		delete(c.connPool, addr)
		c.mu.Unlock()
	}
	return err
}

func (c *GatewayClient) sendAndRecv(addr string, gd *protocol.GatewayData) ([]byte, error) {
	conn, err := c.getConn(addr)
	if err != nil {
		return nil, err
	}
	data := protocol.Encode(gd)
	encrypted, err := crypto.Encrypt(data, c.aesKey)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	if _, err = conn.Write(buf); err != nil {
		c.mu.Lock()
		delete(c.connPool, addr)
		c.mu.Unlock()
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	rLenBuf := make([]byte, 4)
	if _, err = io.ReadFull(conn, rLenBuf); err != nil {
		return nil, err
	}
	rLen := binary.BigEndian.Uint32(rLenBuf)
	ciphertext := make([]byte, rLen)
	if _, err = io.ReadFull(conn, ciphertext); err != nil {
		return nil, err
	}
	plaintext, err := crypto.Decrypt(ciphertext, c.aesKey)
	if err != nil {
		return nil, err
	}
	// Response format: 4-byte length + data
	if len(plaintext) < 4 {
		return plaintext, nil
	}
	dataLen := binary.BigEndian.Uint32(plaintext[:4])
	if int(dataLen)+4 <= len(plaintext) {
		return plaintext[4 : 4+dataLen], nil
	}
	return plaintext, nil
}

func (c *GatewayClient) sendToAllGateways(gd *protocol.GatewayData) error {
	addrs, err := c.getGatewayAddresses()
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		c.sendToGateway(addr, gd)
	}
	return nil
}

func addrStr(ip uint32, port uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d:%d", ip>>24, (ip>>16)&0xff, (ip>>8)&0xff, ip&0xff, port)
}

// ----- Public API -----

func (c *GatewayClient) SendToClient(clientID string, message []byte) error {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToOne
	gd.ConnectionID = connID
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	return c.sendToGateway(addrStr(ip, port), gd)
}

func (c *GatewayClient) SendToAll(message []byte) error {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToAll
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	return c.sendToAllGateways(gd)
}

func (c *GatewayClient) SendToUID(uid interface{}, message []byte) error {
	var uids []interface{}
	switch v := uid.(type) {
	case []interface{}:
		uids = v
	default:
		uids = []interface{}{v}
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToUID
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	ext, _ := json.Marshal(uids)
	gd.ExtData = string(ext)
	return c.sendToAllGateways(gd)
}

func (c *GatewayClient) SendToGroup(group interface{}, message []byte) error {
	var groups []interface{}
	switch v := group.(type) {
	case []interface{}:
		groups = v
	default:
		groups = []interface{}{v}
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToGroup
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	ext, _ := json.Marshal(map[string]interface{}{"group": groups, "exclude": nil})
	gd.ExtData = string(ext)
	return c.sendToAllGateways(gd)
}

func (c *GatewayClient) BindUID(clientID, uid string) error {
	return c.sendCmdToClient(clientID, protocol.CmdBindUID, nil, uid)
}

func (c *GatewayClient) UnbindUID(clientID, uid string) error {
	return c.sendCmdToClient(clientID, protocol.CmdUnbindUID, nil, uid)
}

func (c *GatewayClient) JoinGroup(clientID, group string) error {
	return c.sendCmdToClient(clientID, protocol.CmdJoinGroup, nil, group)
}

func (c *GatewayClient) LeaveGroup(clientID, group string) error {
	return c.sendCmdToClient(clientID, protocol.CmdLeaveGroup, nil, group)
}

func (c *GatewayClient) CloseClient(clientID string, message []byte) error {
	return c.sendCmdToClient(clientID, protocol.CmdKick, message, "")
}

func (c *GatewayClient) DestroyClient(clientID string) error {
	return c.sendCmdToClient(clientID, protocol.CmdDestroy, nil, "")
}

func (c *GatewayClient) SetSession(clientID string, session map[string]interface{}) error {
	return c.sendCmdToClient(clientID, protocol.CmdSetSession, nil, gwctx.SessionEncode(session))
}

func (c *GatewayClient) UpdateSession(clientID string, session map[string]interface{}) error {
	return c.sendCmdToClient(clientID, protocol.CmdUpdateSession, nil, gwctx.SessionEncode(session))
}

func (c *GatewayClient) IsOnline(clientID string) (bool, error) {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return false, err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdIsOnline
	gd.ConnectionID = connID
	resp, err := c.sendAndRecv(addrStr(ip, port), gd)
	if err != nil {
		return false, err
	}
	var v int
	json.Unmarshal(resp, &v)
	return v == 1, nil
}

func (c *GatewayClient) GetAllClientCount() (int, error) {
	addrs, err := c.getGatewayAddresses()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, addr := range addrs {
		gd := protocol.NewEmptyData()
		gd.Cmd = protocol.CmdGetClientCountByGroup
		resp, err := c.sendAndRecv(addr, gd)
		if err != nil {
			continue
		}
		var count int
		json.Unmarshal(resp, &count)
		total += count
	}
	return total, nil
}

func (c *GatewayClient) sendCmdToClient(clientID string, cmd uint8, body []byte, extData string) error {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = cmd
	gd.ConnectionID = connID
	gd.Body = body
	gd.ExtData = extData
	gd.Flag = protocol.FlagBodyIsScalar
	return c.sendToGateway(addrStr(ip, port), gd)
}
