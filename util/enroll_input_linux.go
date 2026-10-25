package util

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func readEnrollmentFile(ctx context.Context, f *os.File) (string, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return "", fmt.Errorf("cannot read enrollment input")
	}
	var token string
	var readErr error
	err = raw.Control(func(fd uintptr) {
		flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil {
			readErr = fmt.Errorf("cannot read enrollment input")
			return
		}
		if err := unix.SetNonblock(int(fd), true); err != nil {
			readErr = fmt.Errorf("cannot make enrollment input cancellable")
			return
		}
		defer unix.FcntlInt(fd, unix.F_SETFL, flags)
		token, readErr = ReadEnrollmentToken(&enrollmentFileReader{ctx: ctx, fd: int(fd)})
	})
	runtime.KeepAlive(f)
	if err != nil {
		return "", fmt.Errorf("cannot read enrollment input")
	}
	return token, readErr
}

type enrollmentFileReader struct {
	ctx context.Context
	fd  int
}

func (r *enrollmentFileReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 50)
		if err == unix.EINTR || n == 0 && err == nil {
			continue
		}
		if err != nil {
			return 0, err
		}
		n, err = unix.Read(r.fd, p)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if n == 0 && err == nil {
			err = io.EOF
		}
		return n, err
	}
}
