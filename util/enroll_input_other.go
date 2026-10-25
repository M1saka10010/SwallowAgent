//go:build !linux && !windows

package util

import (
	"context"
	"fmt"
	"os"
	"time"
)

func readEnrollmentFile(ctx context.Context, f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("cannot read enrollment input")
	}
	if info.Mode().IsRegular() {
		return ReadEnrollmentToken(f)
	}
	if err := f.SetReadDeadline(time.Time{}); err != nil {
		return "", fmt.Errorf("enrollment input does not support cancellation; redirect a regular file instead")
	}
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
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return token, err
}
