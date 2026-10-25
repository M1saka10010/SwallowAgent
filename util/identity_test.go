package util

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if _, err := LoadPrivateKey(path); !os.IsNotExist(err) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
	pub, err := GenerateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	key, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if PublicKeyString(key) != pub {
		t.Fatal("public key mismatch")
	}
	if _, err = GenerateKey(path); !os.IsExist(err) {
		t.Fatalf("expected exists error, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("key changed")
	}
	for _, data := range [][]byte{[]byte("bad"), bytes.Repeat([]byte("x"), 4097), append(append([]byte{}, original...), original...), append([]byte("junk\n"), original...), append([]byte("-----BEGIN PRIVATE KEY-----\ninvalid\n"), original...)} {
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadPrivateKey(path); err == nil {
			t.Fatal("accepted invalid key")
		}
	}
	if _, err = LoadPrivateKey(filepath.Dir(path)); err == nil {
		t.Fatal("accepted directory")
	}
}
