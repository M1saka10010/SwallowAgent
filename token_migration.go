//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/M1saka10010/SwallowAgent/model"
	"github.com/M1saka10010/SwallowAgent/util"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func migrationConfig(path, key, url string, dev bool) (model.Config, bool, error) {
	var old struct {
		model.Config `yaml:",inline"`
		Token        string `yaml:"token"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return old.Config, false, fmt.Errorf("cannot read token migration configuration")
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if d.Decode(&old) != nil || d.Decode(new(interface{})) != io.EOF {
		return old.Config, false, fmt.Errorf("invalid token migration YAML (unknown fields, invalid types or multiple documents)")
	}
	legacy := old.PrivateKeyPath == ""
	var fields map[string]yaml.Node
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return old.Config, false, fmt.Errorf("invalid migration configuration")
	}
	if _, exists := fields["privateKeyPath"]; key != "" && exists {
		return old.Config, false, fmt.Errorf("source must not contain privateKeyPath")
	}
	if legacy && strings.TrimSpace(old.Token) == "" {
		return old.Config, false, fmt.Errorf("source must contain a nonempty token")
	}
	if key != "" && !legacy {
		return old.Config, false, fmt.Errorf("already public-key configured; not a Token migration")
	}
	if legacy {
		old.PrivateKeyPath = key
	}
	if url != "" {
		old.ReportURL = url
	}
	old.DevMode = old.DevMode || dev
	if err := validateConfig(&old.Config); err != nil {
		return old.Config, legacy, fmt.Errorf("invalid token migration configuration")
	}
	if !filepath.IsAbs(old.PrivateKeyPath) {
		old.PrivateKeyPath = filepath.Join(filepath.Dir(path), old.PrivateKeyPath)
	}
	old.PrivateKeyPath, err = filepath.Abs(old.PrivateKeyPath)
	if err != nil {
		return old.Config, legacy, fmt.Errorf("invalid private key path")
	}
	return old.Config, legacy, nil
}

func runTokenMigration(old, output, key, url, enroll string, dev, token bool) error {
	if enroll != "" {
		if old != "" || output != "" || key != "" || url != "" || dev || !token {
			return fmt.Errorf("invalid enrollment options")
		}
		c, legacy, err := migrationConfig(enroll, "", "", false)
		if err != nil {
			return err
		}
		if legacy {
			return fmt.Errorf("missing private key")
		}
		input := os.Stdin
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		_, err = util.Enroll(ctx, c.ReportURL, c.PrivateKeyPath, input, c.DevMode)
		if err != nil {
			return err
		}
		return migrationReportHealth(ctx, c)
	}
	if old == "" || output == "" || key == "" {
		return fmt.Errorf("prepare requires old configuration, output and default private key")
	}
	c, legacy, err := migrationConfig(old, key, url, dev)
	if err != nil {
		return err
	}
	if !legacy || !token {
		return fmt.Errorf("legacy migration requires --enrollment-token-stdin for the original host")
	}
	// Refuse writable or redirected key directories before creating an identity.
	for p := filepath.Dir(c.PrivateKeyPath); p != "/"; p = filepath.Dir(p) {
		s, err := os.Lstat(p)
		if err != nil || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe private key directory")
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) || (s.Mode().Perm()&0022 != 0 && s.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("unsafe private key directory ownership or permissions")
		}
	}
	if s, err := os.Lstat(c.PrivateKeyPath); err == nil {
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("unsafe private key owner")
		}
	}
	_, err = util.LoadOrGenerateKey(c.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("cannot prepare private key: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("cannot encode token migration configuration")
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func migrationReportHealth(ctx context.Context, c model.Config) error {
	key, err := util.LoadPrivateKey(c.PrivateKeyPath)
	if err != nil {
		return err
	}
	client, err := util.NewWSClient(c.ReportURL, key, c.DevMode)
	if err != nil {
		return err
	}
	defer client.Close()
	type sample struct {
		info  any
		usage any
		err   error
	}
	collected := make(chan sample, 1)
	// Collectors have no context API; a blocked OS query must not hold migration open.
	go func() {
		info, err := util.GetSystemInfo()
		if err != nil {
			collected <- sample{err: err}
			return
		}
		util.PrimeCPUSample()
		usage, err := util.GetUsageInfo(nil, c.Paths, c.InterfaceNames)
		collected <- sample{info: info, usage: usage, err: err}
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("migration report health: %w", ctx.Err())
	case data := <-collected:
		if data.err != nil {
			return fmt.Errorf("migration collection failed: %w", data.err)
		}
		if err := client.Send(ctx, "system_info", data.info); err != nil {
			return fmt.Errorf("migration system info report failed: %w", err)
		}
		if err := client.Send(ctx, "system_usage", data.usage); err != nil {
			return fmt.Errorf("migration usage report failed: %w", err)
		}
	}
	// The report protocol has no ingestion ACK; successful writes are not a persistence receipt.
	return nil
}
