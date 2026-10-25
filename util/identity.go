package util

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
)

func PublicKeyString(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func GenerateKey(path string) (string, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	f, err := createPrivateKeyFile(path)
	if err != nil {
		return "", err
	}
	err = pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
	closeErr := f.Close()
	if err != nil {
		return "", incompletePrivateKeyError(path, err)
	}
	if closeErr != nil {
		return "", incompletePrivateKeyError(path, closeErr)
	}
	return PublicKeyString(key), nil
}

func incompletePrivateKeyError(path string, err error) error {
	return fmt.Errorf("private key %q may be incomplete; inspect the file before retrying: %w", path, err)
}

func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	f, err := openPrivateKeyFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	if len(data) > 4096 {
		return nil, fmt.Errorf("private key file too large")
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(rest) != 0 || !bytes.HasPrefix(data, []byte("-----BEGIN PRIVATE KEY-----")) || bytes.Count(data, []byte("-----BEGIN PRIVATE KEY-----")) != 1 {
		return nil, fmt.Errorf("expected a single PKCS8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key must be Ed25519")
	}
	return key, nil
}
