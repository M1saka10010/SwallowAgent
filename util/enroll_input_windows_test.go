package util

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWindowsSynchronousEnrollmentPipe(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		var read, write syscall.Handle
		if err := syscall.CreatePipe(&read, &write, nil, 0); err != nil {
			t.Fatal(err)
		}
		r := os.NewFile(uintptr(read), "stdin")
		w := os.NewFile(uintptr(write), "writer")
		t.Cleanup(func() { r.Close(); w.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		done := make(chan error, 1)
		go func() {
			token, err := ReadEnrollmentTokenContext(ctx, r)
			if !cancelRead && err == nil && token != strings.Repeat("a", 64) {
				err = errors.New("incorrect token")
			}
			done <- err
		}()
		if cancelRead {
			time.Sleep(100 * time.Millisecond)
			cancel()
		} else {
			if _, err := w.Write([]byte(strings.Repeat("a", 64) + "\r\n")); err != nil {
				t.Fatal(err)
			}
			w.Close()
		}
		select {
		case err := <-done:
			if cancelRead && !errors.Is(err, context.Canceled) || !cancelRead && err != nil {
				t.Fatalf("cancel=%v: %v", cancelRead, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("synchronous pipe did not return")
		}
	}
}
