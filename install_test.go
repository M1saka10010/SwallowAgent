//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallerEnrollmentSignal(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root installer simulation")
	}
	for _, signal := range []string{"TERM", "INT", "HUP"} {
		t.Run(signal, func(t *testing.T) {
			root := t.TempDir()
			data, err := os.ReadFile("install.sh")
			if err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(string(data), "/opt/swallow-agent", filepath.Join(root, "agent"))
			script = strings.ReplaceAll(script, "/etc/systemd/system", root)
			write := func(name, data string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("install.sh", script)
			write("binary", "#!/bin/bash\nif [ \"$1\" = -h ]; then echo -enrollment-token-stdin; exit; fi\nprintf key > \"$4\"\ntrap 'touch \"$ROOT/reaped\"; exit 0' TERM\nkill -$SIGNAL $PPID\nwhile :; do :; done\n")
			write("systemctl", "#!/bin/bash\ntouch \"$ROOT/system-called\"\n")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "install.sh"), "--binary", filepath.Join(root, "binary"), "--report-url", "wss://example.com/report", "--enrollment-token-stdin")
			cmd.Env = append(os.Environ(), "ROOT="+root, "SIGNAL="+signal, "PATH="+root+":"+os.Getenv("PATH"))
			cmd.WaitDelay = time.Second
			if out, err := cmd.CombinedOutput(); err == nil || ctx.Err() != nil {
				t.Fatalf("signal not handled: %v %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(root, "reaped")); err != nil {
				t.Fatal("child not reaped", err)
			}
			if _, err := os.Stat(filepath.Join(root, "agent/agent.key")); err != nil {
				t.Fatal("retry key lost", err)
			}
			for _, name := range []string{"swallow-agent.service", "system-called", "agent/config.yml", "agent/swallow-agent"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatalf("continued install: %s", name)
				}
			}
		})
	}
}

func TestInstallerRetry(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("installer requires root")
	}
	root := t.TempDir()
	installDir := filepath.Join(root, "agent")
	service := filepath.Join(root, "swallow-agent.service")
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(data), "/opt/swallow-agent", installDir)
	script = strings.ReplaceAll(script, "/etc/systemd/system", root)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("install.sh", script)
	write("binary", "#!/bin/bash\nif [ \"$1\" = -h ]; then echo -enrollment-token-stdin; exit; fi\nkey=$4\n[ -f \"$key\" ] || (umask 077; printf key > \"$key\")\ncat >/dev/null\n[ ! -f \"$FAIL_ENROLL\" ]\n")
	write("systemctl", "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$CALL_LOG\"\n[ ! -f \"$FAIL_SYSTEM\" ]\n")
	failEnroll := filepath.Join(root, "fail-enroll")
	failSystem := filepath.Join(root, "fail-system")
	log := filepath.Join(root, "calls")
	run := func(wantSuccess bool) {
		t.Helper()
		cmd := exec.Command("bash", filepath.Join(root, "install.sh"), "--binary", filepath.Join(root, "binary"), "--report-url", "wss://example.com/report", "--enrollment-token-stdin")
		cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "FAIL_ENROLL="+failEnroll, "FAIL_SYSTEM="+failSystem, "CALL_LOG="+log)
		cmd.Stdin = strings.NewReader(strings.Repeat("a", 64))
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("%v: %s", err, out)
		}
	}
	os.WriteFile(failEnroll, nil, 0600)
	run(false)
	if _, err := os.Stat(service); !os.IsNotExist(err) {
		t.Fatal("service installed before enrollment")
	}
	os.Remove(failEnroll)
	os.WriteFile(failSystem, nil, 0600)
	run(false)
	before, _ := os.ReadFile(filepath.Join(installDir, "agent.key"))
	configBefore, _ := os.ReadFile(filepath.Join(installDir, "config.yml"))
	os.Remove(failSystem)
	run(true)
	after, _ := os.ReadFile(filepath.Join(installDir, "agent.key"))
	configAfter, _ := os.ReadFile(filepath.Join(installDir, "config.yml"))
	if string(before) != "key" || string(before) != string(after) || string(configBefore) != string(configAfter) {
		t.Fatal("retry changed key/config")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "enable --now swallow-agent.service") {
		t.Fatal("missing auto start")
	}
	if strings.Contains(string(configAfter), strings.Repeat("a", 64)) {
		t.Fatal("persisted token")
	}
	os.WriteFile(service, []byte("unrelated"), 0644)
	run(false)
	unchanged, _ := os.ReadFile(service)
	if string(unchanged) != "unrelated" {
		t.Fatal("overwrote service")
	}
}

func TestInstallerRejectsUpgrade(t *testing.T) {
	out, err := exec.Command("bash", "install.sh", "--upgrade").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "通用升级不受支持") {
		t.Fatalf("%v %s", err, out)
	}
}
