package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnrollmentCLISignals(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agent")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			probed := make(chan struct{}, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				select {
				case probed <- struct{}{}:
				default:
				}
			}))
			defer s.Close()
			cmd := exec.Command(bin, "-enroll", "ws"+strings.TrimPrefix(s.URL, "http")+"/report", "-private-key", filepath.Join(t.TempDir(), "key"), "-enrollment-token-stdin", "-dev-mode")
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-probed:
			case <-time.After(5 * time.Second):
				t.Fatal("CLI did not probe")
			}
			secret := strings.Repeat("c", 64)
			if _, err := in.Write([]byte(secret)); err != nil {
				t.Fatal(err)
			}
			// Leave stdin open: strict whole-input validation must still await EOF.
			time.Sleep(100 * time.Millisecond)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(out.String(), "context canceled") {
					t.Fatalf("not graceful cancellation: %v, %s", err, out.String())
				}
				if strings.Contains(out.String(), secret) {
					t.Fatal("secret printed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("CLI remained blocked on stdin")
			}
		})
	}
}
