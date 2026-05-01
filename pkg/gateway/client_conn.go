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

type TCPClientConn struct{ conn net.Conn }

func NewTCPClientConn(conn net.Conn) *TCPClientConn { return &TCPClientConn{conn: conn} }
func (c *TCPClientConn) Close() error               { return c.conn.Close() }
func (c *TCPClientConn) RemoteAddr() net.Addr        { return c.conn.RemoteAddr() }
func (c *TCPClientConn) ConnType() string            { return "tcp" }

func (c *TCPClientConn) RemoteIP() string {
	host, _, _ := net.SplitHostPort(c.conn.RemoteAddr().String())
	return host
}

func (c *TCPClientConn) RemotePort() int {
	_, port, _ := net.SplitHostPort(c.conn.RemoteAddr().String())
	p, _ := strconv.Atoi(port)
	return p
}

// Read 读取一个完整包：4字节长度头 + body
func (c *TCPClientConn) Read() ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return nil, err
	}
	length := uint32(header[0])<<24 | uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3])
	if length == 0 {
		return []byte{}, nil
	}
	if length > 10*1024*1024 {
		return nil, io.ErrUnexpectedEOF
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return nil, err
	}
	return body, nil
}

// Write 写入一个完整包：4字节长度头 + body
func (c *TCPClientConn) Write(data []byte) error {
	length := uint32(len(data))
	header := []byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)}
	_, err := c.conn.Write(append(header, data...))
	return err
}
