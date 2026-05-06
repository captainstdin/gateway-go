package gateway

import (
	"io"
	"net"
	"strconv"

	"github.com/gorilla/websocket"
)

// ClientConn 统一的客户端连接接口，抽象 TCP 和 WebSocket
type ClientConn interface {
	Read() ([]byte, error)
	Write(data []byte) error
	Close() error
	RemoteAddr() net.Addr
	RemoteIP() string
	RemotePort() int
	ConnType() string
}

// ----- WebSocket 实现 -----

type WSClientConn struct{ conn *websocket.Conn }

func NewWSClientConn(conn *websocket.Conn) *WSClientConn { return &WSClientConn{conn: conn} }
func (c *WSClientConn) Read() ([]byte, error)            { _, msg, err := c.conn.ReadMessage(); return msg, err }
func (c *WSClientConn) Write(data []byte) error           { return c.conn.WriteMessage(websocket.TextMessage, data) }
func (c *WSClientConn) Close() error                      { return c.conn.Close() }
func (c *WSClientConn) RemoteAddr() net.Addr              { return c.conn.RemoteAddr() }
func (c *WSClientConn) ConnType() string                  { return "websocket" }
func (c *WSClientConn) RawConn() *websocket.Conn          { return c.conn }

func (c *WSClientConn) RemoteIP() string {
	host, _, _ := net.SplitHostPort(c.conn.RemoteAddr().String())
	return host
}

func (c *WSClientConn) RemotePort() int {
	_, port, _ := net.SplitHostPort(c.conn.RemoteAddr().String())
	p, _ := strconv.Atoi(port)
	return p
}

// ----- TCP 实现 -----

// TCPClientConn 通用 TCP 客户端连接，通过 ClientProtocol 实现协议的可插拔。
// 默认使用 LengthFieldProtocol（4字节长度头 + body），也可注入自定义协议。
type TCPClientConn struct {
	conn     net.Conn
	protocol ClientProtocol
	buf      []byte // 接收缓冲区，用于 protocol.Input 分包
}

func NewTCPClientConn(conn net.Conn, proto ClientProtocol) *TCPClientConn {
	return &TCPClientConn{conn: conn, protocol: proto, buf: make([]byte, 0, 4096)}
}
func (c *TCPClientConn) Close() error        { return c.conn.Close() }
func (c *TCPClientConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }
func (c *TCPClientConn) ConnType() string     { return "tcp" }

func (c *TCPClientConn) RemoteIP() string {
	host, _, _ := net.SplitHostPort(c.conn.RemoteAddr().String())
	return host
}

func (c *TCPClientConn) RemotePort() int {
	_, port, _ := net.SplitHostPort(c.conn.RemoteAddr().String())
	p, _ := strconv.Atoi(port)
	return p
}

// Read 通过 protocol.Input 分包，再调用 protocol.Decode 解包，返回业务数据
func (c *TCPClientConn) Read() ([]byte, error) {
	tmp := make([]byte, 4096)
	for {
		// 尝试分包
		n := c.protocol.Input(c.buf)
		if n < 0 {
			return nil, io.ErrUnexpectedEOF // 协议错误
		}
		if n > 0 && len(c.buf) >= n {
			// 完整包就绪，取出并解码
			packet := make([]byte, n)
			copy(packet, c.buf[:n])
			remaining := len(c.buf) - n
			if remaining == 0 {
				c.buf = c.buf[:0]
			} else {
				// 拷贝剩余数据到新 slice，释放旧的底层数组
				newBuf := make([]byte, remaining)
				copy(newBuf, c.buf[n:])
				c.buf = newBuf
			}
			return c.protocol.Decode(packet), nil
		}
		// 数据不够，继续从连接读取
		nr, err := c.conn.Read(tmp)
		if err != nil {
			return nil, err
		}
		c.buf = append(c.buf, tmp[:nr]...)
	}
}

// Write 通过 protocol.Encode 编码后发送给客户端
func (c *TCPClientConn) Write(data []byte) error {
	_, err := c.conn.Write(c.protocol.Encode(data))
	return err
}
