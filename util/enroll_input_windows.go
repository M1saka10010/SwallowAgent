package util

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"time"
)

var enrollmentKernel = syscall.NewLazyDLL("kernel32.dll")
var enrollmentThreadID = enrollmentKernel.NewProc("GetCurrentThreadId")
var enrollmentOpenThread = enrollmentKernel.NewProc("OpenThread")
var enrollmentCancelIO = enrollmentKernel.NewProc("CancelSynchronousIo")

func readEnrollmentFile(ctx context.Context, f *os.File) (string, error) {
	if f.SetReadDeadline(time.Time{}) == nil {
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			_ = f.SetReadDeadline(time.Now())
			close(done)
		})
		token, err := ReadEnrollmentToken(f)
		if !stop() {
			<-done
		}
		_ = f.SetReadDeadline(time.Time{})
		return token, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	id, _, _ := enrollmentThreadID.Call()
	thread, _, _ := enrollmentOpenThread.Call(1, 0, id) // THREAD_TERMINATE is required by CancelSynchronousIo.
	if thread == 0 {
		return "", fmt.Errorf("cannot prepare cancellable enrollment input")
	}
	defer syscall.CloseHandle(syscall.Handle(thread))
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			// Cancellation may precede ReadFile entering the kernel; retry until it returns.
			enrollmentCancelIO.Call(thread)
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	token, err := ReadEnrollmentToken(&enrollmentWindowsReader{ctx: ctx, handle: syscall.Handle(f.Fd())})
	close(done)
	<-stopped
	runtime.KeepAlive(f)
	return token, err
}

type enrollmentWindowsReader struct {
	ctx    context.Context
	handle syscall.Handle
}

func (r *enrollmentWindowsReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	var n uint32
	err := syscall.ReadFile(r.handle, p, &n, nil)
	if err == syscall.ERROR_BROKEN_PIPE || err == nil && n == 0 {
		err = io.EOF
	}
	return int(n), err
}
