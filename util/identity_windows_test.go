package util

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsIdentityAllowsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	pub, err := GenerateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	// The caller, not the agent, controls ACL policy. Everyone-readable is accepted.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatal(err)
	}
	key, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if PublicKeyString(key) != pub {
		t.Fatal("public key mismatch")
	}
}

func TestWindowsIdentityReparsePoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if _, err := GenerateKey(path); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink creation requires Windows privilege/developer mode: %v", err)
	}
	if _, err := LoadPrivateKey(link); err == nil {
		t.Fatal("accepted reparse point")
	}
	if _, err := GenerateKey(link); err == nil {
		t.Fatal("overwrote reparse point")
	}
}
