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

	"github.com/M1saka10010/SwallowAgent/model"
	"github.com/M1saka10010/SwallowAgent/util"
)

func TestMigrationHealthDeadline(t *testing.T) {
	key := filepath.Join(t.TempDir(), "agent.key")
	if _, err := util.GenerateKey(key); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := migrationReportHealth(ctx, model.Config{PrivateKeyPath: key, ReportURL: "ws://127.0.0.1:1/report", DevMode: true})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("health did not honor cancellation: %v", err)
	}
}

func TestTokenMigrationInstaller(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root installer simulation")
	}
	built := filepath.Join(t.TempDir(), "agent")
	if out, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile("migrate-token-to-key.sh")
	for _, mode := range []string{"legacy", "retry-key", "existing", "modern", "inactive", "enroll-fail", "restart-fail", "unstable-fail", "evil-link", "dropin", "invalid-token", "term-fail", "commit-term-fail"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "agent")
			os.Mkdir(dir, 0755)
			os.Chmod(dir, 0755)
			write := func(p, s string, m os.FileMode) {
				t.Helper()
				if err := os.WriteFile(p, []byte(s), m); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(string(data), "/opt/swallow-agent", dir)
			script = strings.ReplaceAll(script, "/etc/systemd/system", root)
			if mode == "commit-term-fail" {
				script = strings.Replace(script, "COMMITTED=true", "kill -TERM $$\nCOMMITTED=true", 1)
			}
			write(filepath.Join(root, "migrate-token-to-key.sh"), script, 0700)
			unit := "[Unit]\nDescription=Swallow Agent\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=" + dir + "\nExecStart=" + dir + "/swallow-agent -c " + dir + "/config.yml\nRestart=on-failure\nRestartSec=5s\n\n[Install]\nWantedBy=multi-user.target\n"
			write(filepath.Join(dir, "swallow-agent.service"), unit, 0644)
			service := filepath.Join(root, "swallow-agent.service")
			link := filepath.Join(dir, "swallow-agent.service")
			if mode == "evil-link" {
				link = filepath.Join(dir, "swallow-agent")
			}
			os.Symlink(link, service)
			if mode == "modern" {
				os.Remove(service)
				write(service, unit, 0644)
				os.Chmod(dir, 0700)
				write(filepath.Join(dir, ".enrollment-install"), "swallow-enrollment-v1 wss://example.com/report false", 0600)
			}
			write(filepath.Join(dir, "swallow-agent"), "old-binary", 0755)
			config := "paths: [/data]\ninterfaceNames: [eth0]\ninterval: 7\nreportUrl: wss://example.com/report\nisDebug: true\n"
			key := filepath.Join(dir, "agent.key")
			if mode == "existing" || mode == "modern" || mode == "retry-key" {
				if out, err := exec.Command(built, "-generate-key", key).CombinedOutput(); err != nil {
					t.Fatalf("%v %s", err, out)
				}
				if mode == "retry-key" {
					config += "token: old-secret\n"
				} else {
					config += "privateKeyPath: agent.key\n"
				}
			} else {
				config += "token: old-secret\n"
			}
			write(filepath.Join(dir, "config.yml"), config, 0600)
			beforeKey, _ := os.ReadFile(key)
			state := "active"
			if mode == "inactive" {
				state = "inactive"
			}
			write(filepath.Join(root, "state"), state, 0600)
			wrapper := "#!/bin/bash\nif [ \"$1\" = -enroll-token-migration ]; then [ \"$(cat \"$ROOT/state\")\" = inactive ] || exit 8; [ \"$MODE\" != enroll-fail ] || exit 7; exit 0; fi\nexec \"$BUILT\" \"$@\"\n"
			write(filepath.Join(root, "new"), wrapper, 0700)
			if mode == "term-fail" {
				wrapper = strings.Replace(wrapper, "exit 0; fi", "kill -TERM $PPID; exec sleep 30; fi", 1)
				write(filepath.Join(root, "new"), wrapper, 0700)
			}
			mock := "#!/bin/bash\nset -eu\necho \"$*\" >> \"$ROOT/calls\"\ncase $1 in\nshow) case $3 in\nDropInPaths) if [ \"$MODE\" = dropin ]; then echo /custom; fi ;;\nFragmentPath) echo \"$ROOT/agent/swallow-agent.service\" ;;\nActiveState) cat \"$ROOT/state\" ;;\nesac ;;\nstop) echo inactive > \"$ROOT/state\" ;;\nrestart) if [ \"$MODE\" = restart-fail ] && [ ! -f \"$ROOT/failed\" ]; then touch \"$ROOT/failed\"; exit 1; fi; echo active > \"$ROOT/state\" ;;\nis-active) [ \"$(cat \"$ROOT/state\")\" = active ] ;;\n*) exit 9 ;;\nesac\n"
			if mode == "unstable-fail" {
				mock = strings.Replace(mock, "is-active) ", `is-active) if [ ! -f "$ROOT/first-active" ]; then touch "$ROOT/first-active"; elif [ ! -f "$ROOT/unstable" ]; then touch "$ROOT/unstable"; exit 1; fi; `, 1)
			}
			write(filepath.Join(root, "systemctl"), mock, 0700)
			if mode == "modern" {
				mock = strings.ReplaceAll(mock, "$ROOT/agent/swallow-agent.service", "$ROOT/swallow-agent.service")
				write(filepath.Join(root, "systemctl"), mock, 0700)
			}
			args := []string{filepath.Join(root, "migrate-token-to-key.sh"), "--binary", filepath.Join(root, "new")}
			if true {
				args = append(args, "--enrollment-token-stdin", "--report-url", "wss://example.com/report")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.WaitDelay = time.Second
			cmd.Stdin = strings.NewReader(strings.Repeat("a", 64))
			if mode == "invalid-token" {
				cmd.Stdin = strings.NewReader("invalid")
			}
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "ROOT="+root, "MODE="+mode, "BUILT="+built)
			out, err := cmd.CombinedOutput()
			failure := mode == "existing" || mode == "modern" || strings.Contains(mode, "fail") || mode == "evil-link" || mode == "dropin" || mode == "invalid-token"
			if mode == "invalid-token" {
				calls, _ := os.ReadFile(filepath.Join(root, "calls"))
				if strings.Contains(string(calls), "stop") {
					t.Fatal("stopped before validating token")
				}
			}
			if (err != nil) != failure {
				t.Fatalf("%v %s", err, out)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
			binary, _ := os.ReadFile(filepath.Join(dir, "swallow-agent"))
			active, _ := os.ReadFile(filepath.Join(root, "state"))
			if strings.TrimSpace(string(active)) != state {
				t.Fatalf("state changed: %s %s", active, out)
			}
			if failure {
				if string(after) != config || string(binary) != "old-binary" {
					t.Fatal("rollback failed")
				}
			} else {
				if string(binary) == "old-binary" || strings.Contains(string(after), "token:") {
					t.Fatal("not upgraded")
				}
				c, _, err := migrationConfig(filepath.Join(dir, "config.yml"), "", "", false)
				if err != nil || c.Interval != 7 || !c.IsDebug || len(c.Paths) != 1 || c.Paths[0] != "/data" || len(c.InterfaceNames) != 1 || c.InterfaceNames[0] != "eth0" {
					t.Fatal("collection config changed", c, err)
				}
				if len(beforeKey) > 0 {
					afterKey, _ := os.ReadFile(key)
					if string(beforeKey) != string(afterKey) {
						t.Fatal("identity changed")
					}
				}
			}
			gotLink, _ := os.Readlink(service)
			if mode == "enroll-fail" || mode == "restart-fail" || mode == "term-fail" || mode == "commit-term-fail" {
				if _, e := os.Stat(filepath.Join(dir, ".token-to-key-complete")); !os.IsNotExist(e) {
					t.Fatal("rollback retained completion marker")
				}
				write(filepath.Join(root, "migrate-token-to-key.sh"), strings.Replace(script, "kill -TERM $$\nCOMMITTED=true", "COMMITTED=true", 1), 0700)
				retained, e := os.ReadFile(key)
				if e != nil {
					t.Fatal("missing retry key", e)
				}
				write(filepath.Join(root, "new"), strings.ReplaceAll(wrapper, "kill -TERM $PPID; exec sleep 30", "exit 0"), 0700)
				retry := exec.CommandContext(ctx, "bash", args...)
				retry.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "ROOT="+root, "MODE=legacy", "BUILT="+built)
				retry.Stdin = strings.NewReader(strings.Repeat("a", 64))
				if out, e := retry.CombinedOutput(); e != nil {
					t.Fatalf("retry: %v %s", e, out)
				}
				recovered, _ := os.ReadFile(key)
				if string(retained) != string(recovered) {
					t.Fatal("retry changed identity")
				}
			}
			if !failure {
				write(filepath.Join(root, "new"), "#!/bin/bash\nexit 99\n", 0700)
				rerun := exec.CommandContext(ctx, "bash", args...)
				rerun.Env = cmd.Env
				if out, e := rerun.CombinedOutput(); e != nil {
					t.Fatalf("completed rerun: %v %s", e, out)
				}
				unchanged, _ := os.ReadFile(filepath.Join(dir, "swallow-agent"))
				if string(unchanged) != string(binary) {
					t.Fatal("rerun updated binary")
				}
				info, e := os.Stat(filepath.Join(dir, ".token-to-key-complete"))
				if e != nil || info.Mode().Perm() != 0600 {
					t.Fatal("unsafe completion marker", e)
				}
			}
			if mode != "modern" && gotLink != link {
				t.Fatal("link changed")
			}
			backups, _ := filepath.Glob(filepath.Join(dir, ".token-migration.*"))
			for _, b := range backups {
				if _, err := os.Stat(filepath.Join(b, "enrollment-token")); !os.IsNotExist(err) {
					t.Fatal("temporary token retained")
				}
				s, _ := os.Stat(b)
				if s.Mode().Perm() != 0700 {
					t.Fatal("public backup")
				}
				for _, f := range []string{"binary", "config", "unit"} {
					s, e := os.Stat(filepath.Join(b, f))
					if e == nil && s.Mode().Perm() != 0600 {
						t.Fatal("public backup file")
					}
				}
			}
		})
	}
}

func TestTokenMigrationStrictConfig(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "config.yml")
	base := "paths: [/data]\ninterfaceNames: [eth0]\ninterval: 7\nreportUrl: wss://example.com/report\nisDebug: true\ntoken: secret-old-token\n"
	for _, source := range []string{strings.ReplaceAll(base, "token: secret-old-token\n", ""), base + "privateKeyPath: ''\n", base + "privateKeyPath: agent.key\n"} {
		os.WriteFile(old, []byte(source), 0600)
		if err := runTokenMigration(old, filepath.Join(dir, "output"), filepath.Join(dir, "key"), "", "", false, true); err == nil {
			t.Fatal("non-Token source accepted")
		}
	}
	for _, extra := range []string{"unknown: secret-old-token\n", "interval: 8\n", "---\ninterval: 8\n", "devMode: [secret-old-token]\n"} {
		os.WriteFile(old, []byte(base+extra), 0600)
		_, _, err := migrationConfig(old, filepath.Join(dir, "key"), "", false)
		if err == nil || strings.Contains(err.Error(), "secret-old-token") {
			t.Fatal("unsafe parse result", err)
		}
	}
	os.WriteFile(old, []byte(base+"privateKeyPath: relative.key\ndevMode: true\n"), 0600)
	c, legacy, err := migrationConfig(old, "", "", false)
	if err != nil || legacy || c.PrivateKeyPath != filepath.Join(dir, "relative.key") || !c.DevMode || !c.IsDebug || c.Interval != 7 || c.Paths[0] != "/data" || c.InterfaceNames[0] != "eth0" {
		t.Fatal(c, legacy, err)
	}
	os.WriteFile(old, []byte(strings.ReplaceAll(base, "wss://example.com", "ws://example.com")), 0600)
	if _, _, err = migrationConfig(old, filepath.Join(dir, "key"), "", false); err == nil {
		t.Fatal("production plaintext accepted")
	}
}
