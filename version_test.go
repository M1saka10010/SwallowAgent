package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCLI(t *testing.T) {
	for _, version := range []string{"dev", "v1.2.3-rc.4+build.5"} {
		t.Run(version, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "agent.exe")
			args := []string{"build", "-o", binary}
			ldflags := ""
			if version != "dev" {
				ldflags = "-s -w -X github.com/M1saka10010/SwallowAgent/buildinfo.Version=" + version
				args = append(args, "-ldflags", ldflags)
			}
			args = append(args, ".")
			if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			for _, flag := range []string{"-version", "--version", "-version=true"} {
				cmd := exec.Command(binary, flag)
				cmd.Dir = t.TempDir()
				out, err := cmd.CombinedOutput()
				if err != nil || string(out) != version+"\n" {
					t.Fatalf("%s: %v, got %q, want %q", flag, err, out, version)
				}
			}
			for _, other := range [][]string{
				{"-c", "missing.yaml"}, {"-generate-key", "key"}, {"-show-public-key", "key"},
				{"-enroll", "invalid"}, {"-private-key", "key"}, {"-enrollment-token-stdin"},
				{"-dev-mode"}, {"-prepare-token-migration", "old"}, {"-output-config", "new"},
				{"-default-private-key", "key"}, {"-report-url", "invalid"},
				{"-enroll-token-migration", "new"}, {"-check-enrollment-token"},
				{"extra"}, {"-c="}, {"-dev-mode=false"},
			} {
				for _, versionFlag := range []string{"-version", "-version=false"} {
					cmd := exec.Command(binary, append([]string{versionFlag}, other...)...)
					cmd.Dir = t.TempDir()
					if out, err := cmd.CombinedOutput(); err == nil || !strings.HasPrefix(string(out), "-version 必须单独使用") {
						t.Fatalf("%v: err=%v output=%q", cmd.Args, err, out)
					}
				}
			}
			if out, err := exec.Command("go", "test", "-count=1", "-ldflags", ldflags, "./util", "-run", "^TestSystemInfoAgentVersion$").CombinedOutput(); err != nil {
				t.Fatalf("JSON version test: %v\n%s", err, out)
			}
		})
	}
}
