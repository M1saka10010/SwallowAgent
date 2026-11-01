package maintenance

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in package test: only run inside a disposable container.
func TestDockerUnattendedAPT(t *testing.T) {
	if os.Getenv("SWALLOW_DOCKER_APT_TEST") != "1" {
		t.Skip("disposable Docker container only")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("Docker required")
	}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(name string, args ...string) {
		t.Helper()
		out, err := command(name, args...).CombinedOutput()
		t.Logf("%s %v\n%s", name, args, out)
		if err != nil {
			t.Fatal(err)
		}
	}
	root := "/tmp/swallow-apt-fixture"
	makeDeb := func(version, postinst string) string {
		dir := filepath.Join(root, "v"+version)
		write(dir+"/DEBIAN/control", "Package: swallow-unattended-fixture\nVersion: "+version+"\nArchitecture: all\nMaintainer: Test <test@example.invalid>\nDescription: disposable fixture\n", 0644)
		write(dir+"/DEBIAN/conffiles", "/etc/swallow-unattended-fixture.conf\n", 0644)
		write(dir+"/etc/swallow-unattended-fixture.conf", "upstream="+version+"\n", 0644)
		write(dir+"/DEBIAN/postinst", postinst, 0755)
		deb := filepath.Join(root, "fixture-"+version+".deb")
		invoke("/usr/bin/dpkg-deb", "--build", dir, deb)
		return deb
	}
	good := "#!/bin/sh\nset -eu\n[ \"$DEBIAN_FRONTEND\" = noninteractive ]\n[ \"$NEEDRESTART_MODE\" = l ]\nif read -r answer; then exit 40; fi\necho fixture-configured\n"
	first := makeDeb("1.0", good)
	invoke("/usr/bin/apt-get", "-y", "install", first)
	write("/etc/swallow-unattended-fixture.conf", "local-customization=keep\n", 0644)
	publish := func(deb, version string) {
		b, err := os.ReadFile(deb)
		if err != nil {
			t.Fatal(err)
		}
		write(root+"/Packages", fmt.Sprintf("Package: swallow-unattended-fixture\nVersion: %s\nArchitecture: all\nMaintainer: Test <test@example.invalid>\nFilename: %s\nSize: %d\nSHA256: %x\nDescription: disposable fixture\n\n", version, filepath.Base(deb), len(b), sha256.Sum256(b)), 0644)
	}
	write("/etc/apt/sources.list", "deb [trusted=yes] file:"+root+" ./\n", 0644)
	write("/etc/apt/apt.conf.d/99swallow-fixture", "Dir::Etc::sourceparts \"-\";\n", 0644)
	second := makeDeb("2.0", good)
	publish(second, "2.0")
	verified := false
	if err := updateSystem("/usr/bin/apt-get", run, func() error { verified = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("verification not called")
	}
	conf, err := os.ReadFile("/etc/swallow-unattended-fixture.conf")
	if err != nil || string(conf) != "local-customization=keep\n" {
		t.Fatalf("config overwritten: %q %v", conf, err)
	}
	out, err := command("/usr/bin/dpkg-query", "-W", "-f=${Version}", "swallow-unattended-fixture").CombinedOutput()
	if err != nil || string(out) != "2.0" {
		t.Fatalf("version %q: %v", out, err)
	}
	t.Log("PASS: apt upgrade 1.0 -> 2.0, local config preserved, environment and stdin EOF checked")
	bad := makeDeb("3.0", "#!/bin/sh\nset -eu\necho manual-answer-required >&2\nread -r answer || exit 42\nexit 43\n")
	publish(bad, "3.0")
	verified = false
	err = updateSystem("/usr/bin/apt-get", run, func() error { verified = true; return nil })
	if err == nil || verified || !strings.Contains(err.Error(), "manual-answer-required") {
		t.Fatalf("expected failure, got %v verified=%v", err, verified)
	}
	t.Logf("PASS: mandatory input fails on EOF without retry: %v", err)
}
