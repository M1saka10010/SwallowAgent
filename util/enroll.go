package util

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func LoadOrGenerateKey(path string) (ed25519.PrivateKey, error) {
	key, err := LoadPrivateKey(path)
	if !os.IsNotExist(err) {
		return key, err
	}
	if _, err = GenerateKey(path); err != nil && !os.IsExist(err) {
		return nil, err
	}
	return LoadPrivateKey(path)
}

func ReadEnrollmentToken(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 67))
	if err != nil {
		return "", fmt.Errorf("cannot read enrollment token")
	}
	token := string(data)
	if strings.HasSuffix(token, "\r\n") {
		token = strings.TrimSuffix(token, "\r\n")
	} else {
		token = strings.TrimSuffix(token, "\n")
	}
	if len(token) != 64 {
		return "", fmt.Errorf("enrollment token must be 64 lowercase hex characters")
	}
	for _, c := range token {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("enrollment token must be 64 lowercase hex characters")
		}
	}
	return token, nil
}

// ReadEnrollmentTokenContext accepts finite in-memory readers, files, and streams
// whose Close promptly interrupts Read. Cancellation may close the stream.
// Other readers are rejected because io.Reader alone cannot promise cancellation.
// File cancellation covers normal pipe/TTY I/O, not stalled filesystem operations.
func ReadEnrollmentTokenContext(ctx context.Context, r io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f, ok := r.(*os.File); ok {
		token, err := readEnrollmentFile(ctx, f)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return token, err
	}
	switch r.(type) {
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		return ReadEnrollmentToken(r)
	}
	c, ok := r.(io.ReadCloser)
	if !ok {
		return "", fmt.Errorf("enrollment input must support cancellation")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.Close()
		close(done)
	})
	token, err := ReadEnrollmentToken(c)
	if !stop() {
		<-done
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return token, err
}

func ProbeReport(ctx context.Context, reportURL string, key ed25519.PrivateKey, devMode bool) error {
	client, err := NewWSClient(reportURL, key, devMode)
	if err != nil {
		return err
	}
	defer client.Close()
	client.mu.Lock()
	err = client.connect(ctx)
	client.mu.Unlock()
	return err
}

// An empty public ID means the existing key authenticated without enrollment.
func Enroll(ctx context.Context, reportURL, keyPath string, tokenInput io.Reader, devMode bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateReportURL(reportURL, devMode); err != nil {
		return "", err
	}
	key, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		return "", err
	}
	if ProbeReport(ctx, reportURL, key, devMode) == nil {
		return "", nil
	}
	token, err := ReadEnrollmentTokenContext(ctx, tokenInput)
	if err != nil {
		return "", err
	}
	id, err := postEnrollment(ctx, reportURL, key, token)
	if err != nil && ProbeReport(ctx, reportURL, key, devMode) == nil {
		return "", nil
	}
	return id, err
}

func postEnrollment(ctx context.Context, reportURL string, key ed25519.PrivateKey, token string) (string, error) {
	u, _ := url.Parse(reportURL)
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = "/enroll"
	publicKey := PublicKeyString(key)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte("swallow-enroll-v1\n"+token+"\n"+publicKey)))
	body, _ := json.Marshal(map[string]string{"token": token, "publicKey": publicKey, "signature": signature})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("cannot create enrollment request")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("enrollment request failed; retry with the same private key")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("enrollment rejected (HTTP %d); retry with the same private key", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) > 4096 {
		return "", fmt.Errorf("invalid enrollment response")
	}
	var result struct {
		PublicID string `json:"publicId"`
	}
	if json.Unmarshal(data, &result) != nil || strings.TrimSpace(result.PublicID) == "" {
		return "", fmt.Errorf("invalid enrollment response")
	}
	return result.PublicID, nil
}
