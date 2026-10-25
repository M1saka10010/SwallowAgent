package util

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWSSCertificateVerification(t *testing.T) {
	testTLSCertificateVerification(t, "wss")
}

func testTLSCertificateVerification(t *testing.T, protocol string) {
	t.Helper()
	for _, mode := range []string{"untrusted", "hostname"} {
		t.Run(mode, func(t *testing.T) {
			var reached atomic.Bool
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
			defer s.Close()
			certPath := filepath.Join(t.TempDir(), "root.pem")
			if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			u := "wss" + strings.TrimPrefix(s.URL, "https") + "/report"
			if mode == "hostname" {
				u = strings.Replace(u, "127.0.0.1", "localhost", 1)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestTLSVerificationHelper$")
			cmd.Env = append(os.Environ(), "SWALLOW_TLS_URL="+u, "SWALLOW_TLS_ROOT="+certPath, "SWALLOW_TLS_MODE="+mode, "SWALLOW_TLS_PROTOCOL="+protocol, "GODEBUG=x509usefallbackroots=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("TLS helper: %v: %s", err, out)
			}
			if reached.Load() {
				t.Fatal("request reached HTTP handler despite invalid certificate")
			}
		})
	}
}

func TestTLSVerificationHelper(t *testing.T) {
	u := os.Getenv("SWALLOW_TLS_URL")
	if u == "" {
		return
	}
	roots := x509.NewCertPool()
	mode := os.Getenv("SWALLOW_TLS_MODE")
	if mode == "hostname" {
		data, err := os.ReadFile(os.Getenv("SWALLOW_TLS_ROOT"))
		if err != nil || !roots.AppendCertsFromPEM(data) {
			t.Fatal("cannot load test root")
		}
	}
	x509.SetFallbackRoots(roots)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := ProbeReport(ctx, u, key, false)
	if mode == "hostname" {
		var hostnameErr x509.HostnameError
		if !errors.As(err, &hostnameErr) {
			t.Fatalf("expected hostname error, got %v", err)
		}
	} else {
		var unknownErr x509.UnknownAuthorityError
		if !errors.As(err, &unknownErr) {
			t.Fatalf("expected trust error, got %v", err)
		}
	}
	if os.Getenv("SWALLOW_TLS_PROTOCOL") == "https" {
		token := strings.Repeat("b", 64)
		_, err = postEnrollment(ctx, u, key, token)
		if err == nil || strings.Contains(err.Error(), token) {
			t.Fatalf("missing or unsafe TLS error: %v", err)
		}
	}
}

func TestReportURL(t *testing.T) {
	for _, tc := range []struct {
		url     string
		dev, ok bool
	}{
		{"wss://example.com/report", false, true}, {"ws://127.0.0.1/report", true, true}, {"ws://[::1]/report", true, true}, {"ws://localhost/report", true, true},
		{"ws://localhost/report", false, false}, {"ws://example.com/report", true, false}, {"wss://example.com/report?token=x", false, false}, {"wss://x/report#", false, false}, {"wss://u:p@x/report", false, false}, {"wss://x/other", false, false}, {"https://x/report", false, false},
	} {
		if got := ValidateReportURL(tc.url, tc.dev) == nil; got != tc.ok {
			t.Errorf("%s: %v", tc.url, got)
		}
	}
}

func testServer(t *testing.T, handler func(*websocket.Conn, *http.Request)) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		handler(c, r)
	}))
	t.Cleanup(s.Close)
	return "ws" + strings.TrimPrefix(s.URL, "http") + "/report"
}

func TestAuthenticatedConcurrentReportsAndReconnect(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	received := make(chan string, 8)
	url := testServer(t, func(c *websocket.Conn, r *http.Request) {
		if r.Header.Get("Token") != "" || r.Header.Get("X-Agent-Public-Key") != base64.StdEncoding.EncodeToString(pub) {
			t.Error("bad headers")
			return
		}
		challenge := base64.StdEncoding.EncodeToString(make([]byte, 32))
		if err := c.WriteJSON(map[string]string{"type": "auth_challenge", "challenge": challenge}); err != nil {
			t.Error(err)
			return
		}
		var response map[string]string
		if err := c.ReadJSON(&response); err != nil {
			t.Error(err)
			return
		}
		sig, err := base64.StdEncoding.DecodeString(response["signature"])
		if err != nil || response["type"] != "auth_response" || !ed25519.Verify(pub, []byte("swallow-report-auth-v1\n"+base64.StdEncoding.EncodeToString(pub)+"\n"+challenge), sig) {
			t.Error("invalid signature")
			return
		}
		if err := c.WriteJSON(map[string]string{"type": "auth_ok"}); err != nil {
			return
		}
		for {
			var msg Message
			if err := c.ReadJSON(&msg); err != nil {
				return
			}
			received <- msg.Type
		}
	})
	client, err := NewWSClient(url, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for round := 0; round < 2; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := client.Send(ctx, "system_info", nil); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		for i := 0; i < 4; i++ {
			select {
			case typ := <-received:
				if typ != "system_info" {
					t.Error(typ)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		client.Close()
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if client.Send(canceled, "system_usage", nil) == nil {
		t.Fatal("accepted canceled send")
	}
}

func TestAuthenticationFailures(t *testing.T) {
	for _, mode := range []string{"bad_type", "bad_nonce", "oversize", "rejected", "missing_ok", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			_, key, _ := ed25519.GenerateKey(rand.Reader)
			url := testServer(t, func(c *websocket.Conn, _ *http.Request) {
				if mode == "cancel" || mode == "timeout" {
					_, _, _ = c.ReadMessage()
					return
				}
				typ, nonce := "auth_challenge", base64.StdEncoding.EncodeToString(make([]byte, 32))
				if mode == "bad_type" {
					typ = "auth_ok"
				}
				if mode == "bad_nonce" {
					nonce = "bad"
				}
				if mode == "oversize" {
					nonce = strings.Repeat("x", 4097)
				}
				_ = c.WriteJSON(map[string]string{"type": typ, "challenge": nonce})
				var response map[string]string
				err := c.ReadJSON(&response)
				if mode == "rejected" || mode == "missing_ok" {
					if err != nil || response["type"] != "auth_response" {
						t.Error("missing auth response")
						return
					}
					if mode == "rejected" {
						_ = c.WriteJSON(map[string]string{"type": "auth_error"})
					}
					if err = c.ReadJSON(&response); err == nil {
						t.Error("report sent before auth_ok")
					}
				} else if err == nil {
					t.Error("accepted invalid challenge")
				}
			})
			client, _ := NewWSClient(url, key, true)
			defer client.Close()
			duration := 200 * time.Millisecond
			if mode == "timeout" {
				duration = 12 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			start := time.Now()
			client.mu.Lock()
			err := client.connect(ctx)
			client.mu.Unlock()
			if err == nil {
				t.Fatal("authentication unexpectedly succeeded")
			}
			if mode == "timeout" && (time.Since(start) < 9*time.Second || time.Since(start) > 11*time.Second) {
				t.Fatal("shared timeout not respected", time.Since(start))
			}
			if client.conn != nil {
				t.Fatal("unauthenticated connection retained")
			}
		})
	}
}
