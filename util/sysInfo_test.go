package util

import (
	"encoding/json"
	"testing"

	"github.com/M1saka10010/SwallowAgent/buildinfo"
)

func TestSystemInfoAgentVersion(t *testing.T) {
	info, err := GetSystemInfo()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := json.Unmarshal(fields["agentVersion"], &version); err != nil {
		t.Fatal(err)
	}
	if version == "" || version != buildinfo.Version {
		t.Fatalf("agentVersion = %q, want %q", version, buildinfo.Version)
	}
}
