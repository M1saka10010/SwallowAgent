package util

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 写超时
const writeTimeout = 10 * time.Second

// 入站认证及应用消息大小上限（字节）。
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
	key     ed25519.PrivateKey

	mu      sync.Mutex // 保护 conn、写操作与 backoff
	conn    *websocket.Conn
	backoff time.Duration // 当前重连退避时长，连接成功后重置为 0
}

// NewWSClient 创建一个 WebSocket 客户端
func NewWSClient(url string, key ed25519.PrivateKey, devMode bool) (*WSClient, error) {
	if err := ValidateReportURL(url, devMode); err != nil {
		return nil, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid Ed25519 private key")
	}
	h := http.Header{}
	h.Set("X-Agent-Public-Key", PublicKeyString(key))
	return &WSClient{
		url:     url,
		headers: h,
		key:     append(ed25519.PrivateKey(nil), key...),
	}, nil
}

func ValidateReportURL(raw string, devMode bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.EscapedPath() != "/report" || u.Opaque != "" {
		return fmt.Errorf("reportUrl requires a host and exact /report path, without userinfo, query or fragment")
	}
	if u.Scheme == "wss" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "ws" && devMode && (strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("reportUrl requires wss; ws requires devMode and a loopback host")
}

// connect 建立连接并启动后台读循环，调用方需持有锁
func (c *WSClient) connect(ctx context.Context) error {
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, _, err := dialer.DialContext(authCtx, c.url, c.headers)
	if err != nil {
		return fmt.Errorf("连接 WebSocket 失败: %w", err)
	}
	conn.SetReadLimit(readLimit)
	stopped := make(chan struct{})
	stop := context.AfterFunc(authCtx, func() { _ = conn.Close(); close(stopped) })
	err = c.authenticate(authCtx, conn)
	if !stop() {
		<-stopped
	}
	if err == nil {
		err = authCtx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("设备认证失败: %w", err)
	}
	if err = conn.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return err
	}
	if err = conn.SetWriteDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return err
	}
	c.conn = conn
	go c.readLoop(ctx, conn)
	return nil
}

func (c *WSClient) authenticate(ctx context.Context, conn *websocket.Conn) error {
	deadline, _ := ctx.Deadline()
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	var challenge struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := conn.ReadJSON(&challenge); err != nil {
		return err
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(challenge.Challenge)
	if challenge.Type != "auth_challenge" || err != nil || len(nonce) != 32 || base64.StdEncoding.EncodeToString(nonce) != challenge.Challenge {
		return fmt.Errorf("invalid authentication challenge")
	}
	message := []byte("swallow-report-auth-v1\n" + PublicKeyString(c.key) + "\n" + challenge.Challenge)
	response := struct {
		Type      string `json:"type"`
		Signature string `json:"signature"`
	}{"auth_response", base64.StdEncoding.EncodeToString(ed25519.Sign(c.key, message))}
	if err := conn.WriteJSON(response); err != nil {
		return err
	}
	var result struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&result); err != nil {
		return err
	}
	if result.Type != "auth_ok" {
		return fmt.Errorf("expected auth_ok")
	}
	return nil
}

// readLoop 读取并丢弃入站消息，使 gorilla/websocket 能正确处理
// ping/pong/close 等控制帧。gorilla 在应用读取时自动回复 ping 以 pong。
// 读取出错（连接关闭、超时、远端关闭等）时丢弃当前连接并退出。
func (c *WSClient) readLoop(ctx context.Context, conn *websocket.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("连接不可用")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	conn := c.conn
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()

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
