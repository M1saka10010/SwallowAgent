package util

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 写超时
const writeTimeout = 10 * time.Second

// 入站消息大小上限（字节）。服务端不应发送数据帧，仅用于限制控制帧，
// 防止恶意服务端发送超大消息导致内存耗尽。
const readLimit int64 = 4096

// 重连退避时间区间
const (
	minReconnectDelay = 1 * time.Second
	maxReconnectDelay = 30 * time.Second
)

// Message 是通过 WebSocket 发送的统一消息封装
type Message struct {
	Type string      `json:"type"` // 消息类型，如 system_info, system_usage
	Data interface{} `json:"data"` // 消息内容
}

// WSClient 是一个带自动重连的 WebSocket 客户端，写操作并发安全。
// 内置后台读循环处理 ping/pong/close 等控制帧。
type WSClient struct {
	url     string
	headers http.Header

	mu      sync.Mutex // 保护 conn、写操作与 backoff
	conn    *websocket.Conn
	backoff time.Duration // 当前重连退避时长，连接成功后重置为 0
}

// NewWSClient 创建一个 WebSocket 客户端
func NewWSClient(url string, headers map[string]string) *WSClient {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &WSClient{
		url:     url,
		headers: h,
	}
}

// connect 建立连接并启动后台读循环，调用方需持有锁
func (c *WSClient) connect(ctx context.Context) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, c.url, c.headers)
	if err != nil {
		return fmt.Errorf("连接 WebSocket 失败: %w", err)
	}
	conn.SetReadLimit(readLimit)
	c.conn = conn
	go c.readLoop(ctx, conn)
	return nil
}

// readLoop 读取并丢弃入站消息，使 gorilla/websocket 能正确处理
// ping/pong/close 等控制帧。gorilla 在应用读取时自动回复 ping 以 pong。
// 读取出错（连接关闭、超时、远端关闭等）时丢弃当前连接并退出。
func (c *WSClient) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// 仅此 goroutine 读取该 conn，无需持有 WSClient.mu
		_, _, err := conn.ReadMessage()
		if err != nil {
			// 连接出错，若仍为当前连接则丢弃以便重连
			c.mu.Lock()
			if c.conn == conn {
				c.dropLocked()
			}
			c.mu.Unlock()
			return
		}
		// 消息已读取并丢弃
	}
}

// ensureConn 确保存在可用连接，必要时带退避重连，直到成功或 ctx 取消。
// 退避时长作为字段保留，跨调用持久化；连接成功后重置。
func (c *WSClient) ensureConn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := c.connect(ctx); err == nil {
			c.backoff = 0 // 连接成功，重置退避
			return nil
		} else {
			fmt.Println(err)
		}

		// 确定本次退避时长
		if c.backoff == 0 {
			c.backoff = minReconnectDelay
		}
		delay := c.backoff

		// 退避等待，可被 ctx 取消打断
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		// 递增下次退避时长
		c.backoff *= 2
		if c.backoff > maxReconnectDelay {
			c.backoff = maxReconnectDelay
		}
	}
}

// Send 发送一条消息，失败时丢弃当前连接以便下次重连
func (c *WSClient) Send(ctx context.Context, msgType string, data interface{}) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("连接不可用")
	}

	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		c.dropLocked()
		return err
	}

	if err := c.conn.WriteJSON(Message{Type: msgType, Data: data}); err != nil {
		c.dropLocked()
		return fmt.Errorf("发送消息失败: %w", err)
	}
	return nil
}

// dropLocked 关闭并清空当前连接，调用方需持有锁
func (c *WSClient) dropLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// Close 优雅关闭连接
func (c *WSClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(writeTimeout),
		)
		c.dropLocked()
	}
}
