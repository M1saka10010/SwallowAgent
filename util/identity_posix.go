//go:build !windows

package util

import (
	"fmt"
	"os"
)

func createPrivateKeyFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func openPrivateKeyFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("private key must be a regular non-symlink file with mode 0600")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, actual) || !os.SameFile(actual, after) || !after.Mode().IsRegular() || actual.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("private key file changed or has unsafe permissions")
	}
	ok = true
	return f, nil
}
