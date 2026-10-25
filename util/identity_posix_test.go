//go:build !windows

package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityPOSIXPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	_, err := GenerateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPrivateKey(link); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, err = GenerateKey(link); err == nil {
		t.Fatal("overwrote symlink")
	}
	for _, mode := range []os.FileMode{0644, 0400, 0666} {
		if err = os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadPrivateKey(path); err == nil {
			t.Fatalf("accepted %o", mode)
		}
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}
