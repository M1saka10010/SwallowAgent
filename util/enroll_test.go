package util

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnrollmentToken(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		if _, err := ReadEnrollmentToken(strings.NewReader(strings.Repeat("a", 64) + suffix)); err != nil {
			t.Fatal(err)
		}
	}
	for _, token := range []string{"", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 65), strings.Repeat("a", 64) + "\nextra", strings.Repeat("a", 64) + "\r"} {
		if _, err := ReadEnrollmentToken(strings.NewReader(token)); err == nil {
			t.Fatal("accepted invalid token")
		}
	}
}

func TestEnrollmentInputCancellation(t *testing.T) {
	for _, kind := range []string{"pipe", "file"} {
		t.Run(kind, func(t *testing.T) {
			var r io.ReadCloser
			var w io.WriteCloser
			if kind == "pipe" {
				r, w = io.Pipe()
			} else {
				var err error
				r, w, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
			}
			defer r.Close()
			defer w.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := ReadEnrollmentTokenContext(ctx, r); done <- err }()
			// io.Pipe synchronizes with Read; os.Pipe buffers the write.
			if _, err := w.Write([]byte("abc")); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				time.Sleep(100 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("input read did not stop")
			}
		})
	}
}

func TestEnrollmentHTTPSCertificateVerification(t *testing.T) {
	testTLSCertificateVerification(t, "https")
}

func TestEnrollmentHTTP(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	token := strings.Repeat("a", 64)
	for _, mode := range []string{"ok", "reject", "redirect", "large", "badJSON", "missingID", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var redirected atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/enroll" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("bad request")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad body")
					return
				}
				sig, _ := base64.StdEncoding.DecodeString(body["signature"])
				if body["token"] != token || body["publicKey"] != PublicKeyString(key) || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte("swallow-enroll-v1\n"+token+"\n"+PublicKeyString(key)), sig) {
					t.Error("bad signature")
				}
				switch mode {
				case "reject":
					w.WriteHeader(403)
					io.WriteString(w, token)
				case "redirect":
					http.Redirect(w, r, target.URL, 307)
				case "large":
					io.WriteString(w, strings.Repeat("x", 4097))
				case "badJSON":
					io.WriteString(w, "{")
				case "missingID":
					io.WriteString(w, "{}")
				case "timeout":
					<-r.Context().Done()
				default:
					json.NewEncoder(w).Encode(map[string]string{"publicId": "device"})
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			id, err := postEnrollment(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/report", key, token)
			if mode == "ok" {
				if err != nil || id != "device" {
					t.Fatal(id, err)
				}
			} else if err == nil || strings.Contains(err.Error(), token) {
				t.Fatal("missing or unsafe error", err)
			}
			if redirected.Load() {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestEnrollmentRecoveryAndKeyReuse(t *testing.T) {
	for _, mode := range []string{"normal", "lostResponse", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			var registered atomic.Bool
			var posts atomic.Int32
			path := filepath.Join(t.TempDir(), "key")
			key, err := LoadOrGenerateKey(path)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/enroll" {
					posts.Add(1)
					if mode == "rejected" {
						w.WriteHeader(403)
						return
					}
					registered.Store(true)
					if mode == "lostResponse" {
						io.WriteString(w, "{")
						return
					}
					json.NewEncoder(w).Encode(map[string]string{"publicId": "device"})
					return
				}
				if !registered.Load() {
					w.WriteHeader(403)
					return
				}
				c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer c.Close()
				nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
				c.WriteJSON(map[string]string{"type": "auth_challenge", "challenge": nonce})
				var response map[string]string
				if c.ReadJSON(&response) != nil {
					return
				}
				sig, _ := base64.StdEncoding.DecodeString(response["signature"])
				if !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte("swallow-report-auth-v1\n"+PublicKeyString(key)+"\n"+nonce), sig) {
					t.Error("bad report signature")
					return
				}
				c.WriteJSON(map[string]string{"type": "auth_ok"})
				c.ReadMessage()
			}))
			defer server.Close()
			url := "ws" + strings.TrimPrefix(server.URL, "http") + "/report"
			_, err = Enroll(context.Background(), url, path, strings.NewReader(strings.Repeat("a", 64)), true)
			if (err != nil) != (mode == "rejected") {
				t.Fatal(err)
			}
			_, err = Enroll(context.Background(), url, path, strings.NewReader(""), true)
			if (err != nil) != (mode == "rejected") {
				t.Fatal(err)
			}
			if posts.Load() != 1 {
				t.Fatal("posted again on recovery")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("key changed")
			}
		})
	}
}
