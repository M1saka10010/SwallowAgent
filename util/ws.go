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

// WSClient 是一个带自动重连的 WebSocket 客户端，写操作并发安全
type WSClient struct {
	url     string
	headers http.Header

	mu   sync.Mutex // 保护 conn 与写操作
	conn *websocket.Conn
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

// connect 建立连接，调用方需持有锁
func (c *WSClient) connect(ctx context.Context) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, c.url, c.headers)
	if err != nil {
		return fmt.Errorf("连接 WebSocket 失败: %w", err)
	}
	c.conn = conn
	return nil
}

// ensureConn 确保存在可用连接，必要时带退避重连，直到成功或 ctx 取消
func (c *WSClient) ensureConn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	delay := minReconnectDelay
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.connect(ctx); err == nil {
			return nil
		} else {
			fmt.Println(err)
		}

		// 退避等待，可被 ctx 取消打断
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < maxReconnectDelay {
			delay *= 2
			if delay > maxReconnectDelay {
				delay = maxReconnectDelay
			}
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

// Close 关闭连接
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
