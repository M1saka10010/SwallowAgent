package util

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEnrollmentFilePreservesFlags(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		flags := func() int {
			raw, err := r.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var value int
			var flagErr error
			if err := raw.Control(func(fd uintptr) { value, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
				t.Fatal(err)
			}
			if flagErr != nil {
				t.Fatal(flagErr)
			}
			return value
		}
		before := flags()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		t.Cleanup(cancel)
		if !cancelRead {
			if _, err := w.Write([]byte(strings.Repeat("a", 64))); err != nil {
				t.Fatal(err)
			}
			w.Close()
		}
		_, err = ReadEnrollmentTokenContext(ctx, r)
		if cancelRead && !errors.Is(err, context.DeadlineExceeded) || !cancelRead && err != nil {
			t.Fatalf("cancel=%v: %v", cancelRead, err)
		}
		if after := flags(); after != before {
			t.Fatalf("flags changed: %x -> %x", before, after)
		}
	}
}
