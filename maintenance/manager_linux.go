package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type Manager struct {
	dir       string
	supported bool
	launch    func(Task) error
	alive     func(Task) (bool, error)
}

func locked(dir string, fn func(*state) error) error {
	for _, name := range []string{"lock", "state.json", "scope"} {
		st, e := os.Lstat(filepath.Join(dir, name))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("unsafe maintenance file %s", name)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	s, err := readState(dir)
	if err != nil {
		return err
	}
	return fn(&s)
}
func command(name string, args ...string) *exec.Cmd {
	if name == "/usr/bin/systemctl" || name == "/usr/bin/systemd-run" || len(args) == 1 && (args[0] == "-version" || args[0] == "-maintenance-capable") {
		args = append([]string{"-k", "2", "20", name}, args...)
		name = "/usr/bin/timeout"
	}
	c := exec.Command(name, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l"}
	// os/exec connects nil Stdin to os.DevNull; never inherit an interactive
	// input stream or supply blind answers to package maintainer scripts.
	c.Stdin = nil
	return c
}
func unit(t Task) string { return fmt.Sprintf("swallow-maintenance-%x", sha256.Sum256([]byte(t.ID))) }
func packageTool() string {
	for _, p := range []string{"/usr/bin/apt-get", "/usr/bin/dnf", "/usr/bin/yum"} {
		if s, e := os.Stat(p); e == nil && s.Mode().IsRegular() && s.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}
func New(enabled bool, scope string) (*Manager, error) {
	m := &Manager{dir: Directory}
	if !enabled {
		return m, nil
	}
	if os.Geteuid() != 0 || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") || packageTool() == "" {
		return m, nil
	}
	for _, p := range []string{"/run/systemd/system", "/usr/bin/systemd-run", "/usr/bin/systemctl", "/usr/bin/bash", "/usr/bin/timeout", "/opt/swallow-agent/swallow-agent"} {
		if _, err := os.Stat(p); err != nil {
			return m, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe != "/opt/swallow-agent/swallow-agent" {
		return m, nil
	}
	for p := "/opt/swallow-agent"; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("unsafe installation parent %s", p)
		}
		if p == "/" {
			break
		}
	}
	if err = os.MkdirAll(m.dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(m.dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("unsafe maintenance directory")
	}
	if owner, ok := st.Sys().(*syscall.Stat_t); !ok || owner.Uid != 0 {
		return nil, fmt.Errorf("maintenance directory must be root owned")
	}
	binding := fmt.Sprintf("%x", sha256.Sum256([]byte(scope)))
	if err = locked(m.dir, func(s *state) error {
		path := filepath.Join(m.dir, "scope")
		old, e := os.ReadFile(path)
		if os.IsNotExist(e) && len(s.Records) == 0 {
			return atomicWrite(path, []byte(binding))
		}
		if e != nil {
			return e
		}
		if string(old) != binding {
			return fmt.Errorf("maintenance identity changed; explicit local reconciliation required")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	m.supported = true
	m.alive = func(t Task) (bool, error) {
		c := command("/usr/bin/systemctl", "show", "--all", "--property=ActiveState", "--property=LoadState", unit(t)+".service")
		out, err := c.Output()
		if strings.Contains(string(out), "LoadState=not-found") {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		for _, s := range strings.Split(string(out), "\n") {
			if s == "ActiveState=active" || s == "ActiveState=activating" || s == "ActiveState=deactivating" {
				return true, nil
			}
		}
		return false, nil
	}
	m.launch = func(t Task) error {
		data, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		worker := filepath.Join(m.dir, "worker")
		if err = atomicWrite(worker, data); err != nil {
			return err
		}
		if err = os.Chmod(worker, 0700); err != nil {
			return err
		}
		return command("/usr/bin/systemd-run", "--unit="+unit(t), "--property=Type=exec", "--property=UMask=0077", "--property=StandardOutput=null", "--property=StandardError=journal", worker, "-maintenance-worker", t.ID).Run()
	}
	return m, nil
}
func (m *Manager) Handle(kind string, data json.RawMessage) {
	if kind != "maintenance_sync" || !m.supported {
		return
	}
	var reply Reply
	if json.Unmarshal(data, &reply) != nil {
		return
	}
	if err := m.apply(reply); err != nil {
		log.Printf("maintenance: %v", err)
	}
}
func (m *Manager) apply(reply Reply) error {
	return locked(m.dir, func(s *state) error {
		for i := range s.Records {
			r := &s.Records[i]
			for _, a := range reply.Acks {
				if a.TaskID == r.Task.ID && a.Seq > r.Ack && a.Seq <= len(r.Events) {
					r.Ack = a.Seq
				}
			}
		}
		if err := writeState(m.dir, *s); err != nil {
			return err
		}
		t := reply.Task
		if t == nil || !valid(*t) {
			return nil
		}
		for _, r := range s.Records {
			if r.Task.ID == t.ID || !terminal(r.Events[len(r.Events)-1].Status) {
				return nil
			}
		}
		if len(s.Records) >= 4096 {
			return fmt.Errorf("maintenance history full; refusing new tasks")
		}
		r := record{Task: *t}
		appendEvent(&r, "accepted", "Task durably accepted")
		s.Records = append(s.Records, r)
		if err := writeState(m.dir, *s); err != nil {
			return err
		}
		if err := m.launch(*t); err != nil {
			appendEvent(&s.Records[len(s.Records)-1], "unknown", "Worker launch outcome uncertain: "+err.Error())
			return writeState(m.dir, *s)
		}
		return nil
	})
}
func (m *Manager) Batch() (Batch, error) {
	b := Batch{Supported: m.supported, Events: []Event{}}
	if !m.supported {
		return b, nil
	}
	err := locked(m.dir, func(s *state) error {
		for i := range s.Records {
			r := &s.Records[i]
			status := r.Events[len(r.Events)-1].Status
			if !terminal(status) && status != "unknown" {
				active, err := m.alive(r.Task)
				if err != nil {
					log.Printf("maintenance worker reconciliation: %v", err)
					active = false
				}
				if !active {
					appendEvent(r, "unknown", "Worker interrupted; operator reconciliation required")
					if err = writeState(m.dir, *s); err != nil {
						return err
					}
				}
			}
			for _, e := range r.Events[r.Ack:] {
				if len(b.Events) < 16 {
					b.Events = append(b.Events, e)
				}
			}
		}
		return nil
	})
	return b, err
}

type Sender interface {
	Send(context.Context, string, interface{}) error
}

func (m *Manager) Run(ctx context.Context, c Sender) {
	for {
		b, err := m.Batch()
		if err == nil {
			err = c.Send(ctx, "maintenance_sync", b)
		}
		if err != nil {
			log.Printf("maintenance sync: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
