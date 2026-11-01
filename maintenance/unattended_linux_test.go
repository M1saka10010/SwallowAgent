package maintenance

import (
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
)

func TestMaintenanceCommandEOFHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "maintenance-eof-helper" {
		return
	}
	if os.Getenv("DEBIAN_FRONTEND") != "noninteractive" || os.Getenv("NEEDRESTART_MODE") != "l" {
		os.Exit(2)
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) != 0 {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestMaintenanceCommandNoninteractive(t *testing.T) {
	c := command(os.Args[0], "-test.run=^TestMaintenanceCommandEOFHelper$", "--", "maintenance-eof-helper")
	if c.Stdin != nil {
		t.Fatal("stdin must use os/exec null input")
	}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	for _, tool := range []string{"/usr/bin/apt-get", "/usr/bin/dnf", "/usr/bin/yum"} {
		c := command(tool, "-y", "upgrade")
		if c.Path != tool {
			t.Fatalf("package manager must not be wrapped in a killing timeout: %s", c.Path)
		}
	}
}

func TestUnattendedPackagePlans(t *testing.T) {
	for _, tool := range []string{"apt-get", "dnf", "yum"} {
		t.Run(tool, func(t *testing.T) {
			var got [][]string
			err := updateSystem("/usr/bin/"+tool, func(name string, args ...string) error {
				if name != "/usr/bin/"+tool {
					t.Fatal(name)
				}
				got = append(got, append([]string(nil), args...))
				return nil
			}, func() error { got = append(got, []string{"verify"}); return nil })
			want := [][]string{{"-y", "upgrade"}, {"verify"}, {"check"}}
			if tool == "apt-get" {
				want = [][]string{{"update"}, {"-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade"}, {"verify"}, {"check"}}
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%v: %v", got, err)
			}
		})
	}
}

func TestPackageFailureStopsWithoutRetry(t *testing.T) {
	failure := errors.New("package requires manual configuration")
	for failAt := 1; failAt <= 3; failAt++ {
		calls := 0
		err := updateSystem("/usr/bin/apt-get", func(string, ...string) error {
			calls++
			if calls == failAt {
				return failure
			}
			return nil
		}, func() error { return nil })
		if !errors.Is(err, failure) || calls != failAt {
			t.Fatalf("calls %d: %v", calls, err)
		}
	}
}
