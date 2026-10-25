//go:build linux

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgrade(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root mock upgrade")
	}
	for _, layout := range []string{"install", "migration"} {
		for _, scenario := range []string{"active", "inactive", "rollback-stop-dead", "rollback-stop-running", "rollback-stop-timeout", "rollback-stop-pid", "rollback-stop-control", "rollback-stop-job", "rollback-stop-restarting", "rollback-stop-unknown", "start-limit", "stop-running", "start-fail", "health-fail", "signal", "hash", "version", "dropin", "fragment", "reload", "unit", "key-link", "hardlink", "lock", "failed", "key-path", "downgrade", "unknown-version", "probe-exit", "probe-exit-inactive"} {
			t.Run(layout+"/"+scenario, func(t *testing.T) {
				root, err := os.MkdirTemp("/root", "swallow-upgrade-test-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(root)
				write := func(p, s string, mode os.FileMode) {
					t.Helper()
					if err := os.WriteFile(p, []byte(s), mode); err != nil {
						t.Fatal(err)
					}
				}
				dir := filepath.Join(root, "agent")
				os.Mkdir(dir, 0700)
				service := filepath.Join(root, "swallow-agent.service")
				data, err := os.ReadFile("upgrade.sh")
				if err != nil {
					t.Fatal(err)
				}
				script := strings.ReplaceAll(string(data), "/opt/swallow-agent", dir)
				script = strings.ReplaceAll(script, "/etc/systemd/system", root)
				write(filepath.Join(root, "upgrade.sh"), script, 0700)
				binary := filepath.Join(dir, "swallow-agent")
				original := "#!/bin/bash\n# original\necho v1.0.0\n"
				if scenario == "downgrade" {
					original = strings.ReplaceAll(original, "v1.0.0", "v2.0.0")
				}
				if scenario == "unknown-version" {
					original = strings.ReplaceAll(original, "v1.0.0", "dev")
				}
				write(binary, original, 0755)
				config := "privateKeyPath: '" + dir + "/agent.key'\n"
				write(filepath.Join(dir, "config.yml"), config, 0600)
				write(filepath.Join(dir, "agent.key"), "private-key", 0600)
				unit := "[Unit]\nDescription=Swallow Agent\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=" + dir + "\nExecStart=" + binary + " -c " + dir + "/config.yml\nRestart=on-failure\nRestartSec=5s\n"
				unitPath := service
				if layout == "install" {
					unit += "UMask=0077\n"
					write(filepath.Join(dir, ".enrollment-install"), "swallow-enrollment-v1 fixture false\n", 0600)
				} else {
					os.Chmod(dir, 0755)
					unitPath = filepath.Join(dir, "swallow-agent.service")
					os.Symlink(unitPath, service)
					write(filepath.Join(dir, ".token-to-key-complete"), "token-to-key fixture false\n", 0600)
				}
				unit += "\n[Install]\nWantedBy=multi-user.target\n"
				write(unitPath, unit, 0644)
				candidate := "#!/bin/bash\n[ \"$1\" = -version ] || exit 1\necho v1.2.3\n"
				if scenario == "version" {
					candidate = strings.ReplaceAll(candidate, "v1.2.3", "v9.0.0")
				}
				if strings.HasPrefix(scenario, "probe-exit") {
					candidate += "exit 42\n"
				}
				write(filepath.Join(root, "candidate"), candidate, 0700)
				hash := fmt.Sprintf("%x", sha256.Sum256([]byte(candidate)))
				if scenario == "hash" {
					hash = strings.Repeat("0", 64)
				}
				state := "active"
				if scenario == "inactive" || scenario == "probe-exit-inactive" {
					state = "inactive"
				}
				if scenario == "failed" {
					state = "failed"
				}
				write(filepath.Join(root, "state"), state, 0600)
				switch scenario {
				case "unit":
					write(unitPath, unit+"# customized\n", 0644)
				case "key-link":
					os.Remove(filepath.Join(dir, "agent.key"))
					os.Symlink(filepath.Join(root, "candidate"), filepath.Join(dir, "agent.key"))
				case "hardlink":
					os.Link(binary, filepath.Join(root, "hardlink"))
				case "key-path":
					config = "privateKeyPath: /other/key\n"
					write(filepath.Join(dir, "config.yml"), config, 0600)
				}
				write(filepath.Join(root, "systemctl"), `#!/bin/bash
set -eu
case "$1" in
 show)
 if [ "$2" = --all ]; then
   state=$(cat "$ROOT/state"); sub=dead; pid=0; control=0; job=
   if [ "$state" = active ]; then sub=running; pid=123; fi
   if [ -e "$ROOT/rollback" ]; then
     case "$SCENARIO" in
       rollback-stop-pid) pid=123 ;;
       rollback-stop-control) control=123 ;;
       rollback-stop-job) job="42 start" ;;
       rollback-stop-restarting) sub=auto-restart ;;
       rollback-stop-unknown) exit 1 ;;
     esac
   fi
   printf 'ActiveState=%s\nSubState=%s\nMainPID=%s\nControlPID=%s\nJob=%s\n' "$state" "$sub" "$pid" "$control" "$job"
   exit 0
 fi
 case "$3" in
 LoadState) echo loaded ;;
 NeedDaemonReload) if [ "$SCENARIO" = reload ]; then echo yes; else echo no; fi ;;
 DropInPaths) if [ "$SCENARIO" = dropin ]; then echo custom.conf; fi ;;
 FragmentPath) if [ "$SCENARIO" = fragment ]; then echo /unknown; else echo "$UNIT"; fi ;;
 ActiveState) cat "$ROOT/state" ;;
 esac ;;
 stop)
 echo stop >> "$ROOT/actions"
 if [ "$SCENARIO" = stop-running ]; then exit 0; fi
 if [ -e "$ROOT/rollback" ]; then
   case "$SCENARIO" in
     rollback-stop-running) exit 1 ;;
     rollback-stop-timeout) exit 124 ;;
   esac
   echo inactive > "$ROOT/state"
   if [ "$SCENARIO" = rollback-stop-dead ]; then exit 1; fi
   exit 0
 fi
 echo inactive > "$ROOT/state"
 if [ "$SCENARIO" = signal ] && [ ! -e "$ROOT/signalled" ]; then touch "$ROOT/signalled"; kill -TERM "$(ps -o ppid= -p "$PPID" | tr -d ' ')"; fi ;;
 reset-failed)
 [ "$#" = 2 ] && [ "$2" = swallow-agent.service ] || exit 1
 grep -qx "# original" "$ROOT/agent/swallow-agent" || exit 1
 echo reset-failed >> "$ROOT/actions"; rm -f "$ROOT/start-limit" ;;
 start) echo start >> "$ROOT/actions"
 if [ "$SCENARIO" = start-limit ] && ! grep -qx "# original" "$ROOT/agent/swallow-agent"; then touch "$ROOT/start-limit"; echo failed > "$ROOT/state"; exit 1; fi
 [ ! -e "$ROOT/start-limit" ] || exit 1
 if [ "$SCENARIO" = start-fail ] && ! grep -qx "# original" "$ROOT/agent/swallow-agent"; then exit 1; fi; echo active > "$ROOT/state" ;;
 is-active)
 if [[ "$SCENARIO" = rollback-stop-* ]] && ! grep -qx "# original" "$ROOT/agent/swallow-agent"; then touch "$ROOT/rollback"; exit 1; fi
 if [ "$SCENARIO" = health-fail ] && ! grep -qx "# original" "$ROOT/agent/swallow-agent"; then exit 1; fi; grep -qx active "$ROOT/state" ;;
 *) exit 1 ;;
esac
`, 0700)
				write(filepath.Join(root, "sleep"), "#!/bin/sh\nexit 0\n", 0700)
				if scenario == "lock" {
					write(filepath.Join(root, "flock"), "#!/bin/sh\nexit 1\n", 0700)
				}
				cmd := exec.Command("bash", filepath.Join(root, "upgrade.sh"), "--binary", filepath.Join(root, "candidate"), "--version", "v1.2.3", "--sha256", hash)
				cmd.Env = append(os.Environ(), "ROOT="+root, "UNIT="+unitPath, "SCENARIO="+scenario, "PATH="+root+":"+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				success := scenario == "active" || scenario == "inactive"
				if (err == nil) != success {
					t.Fatalf("err=%v output=%s", err, out)
				}
				got, _ := os.ReadFile(binary)
				want := original
				unsafeRollback := strings.HasPrefix(scenario, "rollback-stop-") && scenario != "rollback-stop-dead"
				if success || unsafeRollback {
					want = candidate
				}
				if string(got) != want {
					t.Fatalf("binary not restored: %q: %s", got, out)
				}
				got, _ = os.ReadFile(filepath.Join(root, "state"))
				wantState := state
				if unsafeRollback && scenario != "rollback-stop-running" && scenario != "rollback-stop-timeout" {
					wantState = "inactive"
				}
				if strings.TrimSpace(string(got)) != wantState {
					t.Fatalf("state %q != %q: %s", got, state, out)
				}
				got, _ = os.ReadFile(filepath.Join(dir, "config.yml"))
				if string(got) != config {
					t.Fatal("config changed")
				}
				if scenario != "key-link" {
					got, _ = os.ReadFile(filepath.Join(dir, "agent.key"))
					if string(got) != "private-key" {
						t.Fatal("key changed")
					}
				}
				actions, _ := os.ReadFile(filepath.Join(root, "actions"))
				if unsafeRollback || scenario == "stop-running" {
					if !strings.Contains(string(out), "refusing binary replacement") || !strings.Contains(string(out), "rollback failed") || strings.Contains(string(out), "Original binary restored") || strings.Contains(string(actions), "reset-failed") {
						t.Fatalf("unsafe rollback not clearly refused: %s actions=%s", out, actions)
					}
				}
				if scenario == "rollback-stop-dead" || scenario == "rollback-stop-running" || scenario == "rollback-stop-timeout" {
					if !strings.Contains(string(out), "failed or timed out") {
						t.Fatalf("missing stop warning: %s", out)
					}
				}
				if scenario == "start-limit" || scenario == "rollback-stop-dead" {
					if !strings.Contains(string(out), "Original binary restored") || !strings.Contains(string(actions), "reset-failed\nstart\n") || strings.Contains(string(out), "could not be restarted") {
						t.Fatalf("recovery failed: %s actions=%s", out, actions)
					}
				}
				if !success && !strings.HasPrefix(scenario, "rollback-stop-") && scenario != "start-limit" && scenario != "stop-running" && scenario != "start-fail" && scenario != "health-fail" && scenario != "signal" {
					if _, err := os.Stat(filepath.Join(root, "actions")); !os.IsNotExist(err) {
						t.Fatalf("stopped before preflight completed: %s", out)
					}
				}
			})
		}
	}
}

func TestUpgradeVersionOrdering(t *testing.T) {
	data, err := os.ReadFile("upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	comparison := strings.Split(strings.Split(string(data), "# Compare SemVer")[1], "cp -p ")[0]
	comparison = comparison[strings.Index(comparison, "\n")+1:]
	for _, tc := range []struct {
		old, target string
		allowed     bool
	}{
		{"v1.2.3", "v1.2.3", true},
		{"v1.2.3+one", "v1.2.3+two", true},
		{"v1.2.3", "v1.2.3-rc.1", false},
		{"v1.2.3-rc.1", "v1.2.3", true},
		{"v1.2.3-rc.2", "v1.2.3-rc.10", true},
		{"v1.2.3-rc.10", "v1.2.3-rc.2", false},
		{"v1.2.3-alpha", "v1.2.3-alpha.1", true},
		{"v1.2.3-alpha.1", "v1.2.3-alpha", false},
		{"v1.2.3-1", "v1.2.3-alpha", true},
		{"v1.2.3-alpha", "v1.2.3-1", false},
		{"v1.20.0", "v1.3.0", false},
		{"v999999999999999999999999999.0.0", "v2.0.0", false},
	} {
		t.Run(tc.old+"_"+tc.target, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", "set -euo pipefail; error() { exit 1; }; "+comparison)
			cmd.Env = append(os.Environ(), "CURRENT="+tc.old, "VERSION="+tc.target)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.allowed {
				t.Fatalf("err=%v output=%s", err, out)
			}
		})
	}
}
