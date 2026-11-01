package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

var workerLog io.Writer

type durableOutput struct {
	dir, id   string
	mu        sync.Mutex
	exhausted bool
}

func (w *durableOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if w.exhausted {
		return n, nil
	}
	for len(p) > 0 {
		size := len(p)
		if size > 1800 {
			size = 1800
		}
		message := strings.ToValidUTF8(string(p[:size]), "?")
		p = p[size:]
		truncated := false
		for {
			err := locked(w.dir, func(s *state) error {
				for i := range s.Records {
					r := &s.Records[i]
					if r.Task.ID == w.id {
						if r.Events[len(r.Events)-1].Status == "unknown" {
							return nil
						}
						if len(r.Events) >= 33 {
							truncated = true
							return nil
						}
						if len(r.Events) == 32 {
							message = "Output truncated: durable output limit reached"
							truncated = true
						}
						appendEvent(r, r.Events[len(r.Events)-1].Status, message)
						return writeState(w.dir, *s)
					}
				}
				return fmt.Errorf("missing task")
			})
			if err == nil {
				if truncated {
					w.exhausted = true
				}
				break
			}
			log.Printf("maintenance output persistence failed: %v", err)
			time.Sleep(3 * time.Second)
		}
		if w.exhausted {
			break
		}
	}
	return n, nil
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) > 2048 {
		p = p[len(p)-2048:]
	}
	b.data = append(b.data, p...)
	if len(b.data) > 2048 {
		b.data = b.data[len(b.data)-2048:]
	}
	return n, nil
}
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.data), "?")
}
func run(name string, args ...string) error {
	c := command(name, args...)
	var output tailBuffer
	c.Stdout = &output
	c.Stderr = &output
	if workerLog != nil {
		c.Stdout = io.MultiWriter(&output, workerLog)
		c.Stderr = c.Stdout
	}
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(name), err, output.String())
	}
	return nil
}
func transition(dir, id, status, message string) error {
	bounded := record{}
	appendEvent(&bounded, status, message)
	message = bounded.Events[0].Message
	return locked(dir, func(s *state) error {
		for i := range s.Records {
			r := &s.Records[i]
			if r.Task.ID == id {
				last := r.Events[len(r.Events)-1]
				if last.Status == status && last.Message == message {
					return writeState(dir, *s)
				}
				if terminal(r.Events[len(r.Events)-1].Status) {
					return fmt.Errorf("task already closed")
				}
				appendEvent(r, status, message)
				return writeState(dir, *s)
			}
		}
		return fmt.Errorf("task not found")
	})
}
func Worker(id string) error {
	if os.Geteuid() != 0 || !identifier.MatchString(id) {
		return fmt.Errorf("invalid worker invocation")
	}
	var task Task
	err := locked(Directory, func(s *state) error {
		for i := range s.Records {
			r := &s.Records[i]
			if r.Task.ID == id && len(r.Events) == 1 && r.Events[0].Status == "accepted" {
				task = r.Task
				appendEvent(r, "running", "Independent maintenance worker started")
				return writeState(Directory, *s)
			}
		}
		return fmt.Errorf("task is not accepted")
	})
	if err != nil {
		return err
	}
	workerLog = &durableOutput{dir: Directory, id: id}
	err = execute(task, func() error { return transition(Directory, id, "verifying", "Verifying maintenance outcome") })
	status, message := "succeeded", "Maintenance completed; no reboot requested"
	if err != nil {
		status, message = "failed", err.Error()
	}
	// Keep the independent worker alive until the final outcome is durably recorded.
	for {
		if err = transition(Directory, id, status, message); err == nil {
			return nil
		}
		log.Printf("maintenance final persistence failed: %v", err)
		time.Sleep(3 * time.Second)
	}
}
func execute(t Task, verify func() error) error {
	if t.Kind == "system_update" {
		return updateSystem(packageTool(), run, verify)
	}
	if t.Kind != "agent_upgrade" || !version.MatchString(t.TargetVersion) {
		return fmt.Errorf("unsupported task")
	}
	return upgrade(t, verify)
}

// No timeout kills package managers: interrupting dpkg/rpm can leave an
// incomplete transaction. Unusual scripts that ignore EOF need local inspection.
func updateSystem(tool string, runCommand func(string, ...string) error, verify func() error) error {
	run := runCommand
	if tool == "" {
		return fmt.Errorf("no supported package manager")
	}
	if filepath.Base(tool) == "apt-get" {
		if err := run(tool, "update"); err != nil {
			return err
		}
		if err := run(tool, "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade"); err != nil {
			return err
		}
	} else if filepath.Base(tool) == "dnf" || filepath.Base(tool) == "yum" {
		if err := run(tool, "-y", "upgrade"); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unsupported package manager")
	}
	if err := verify(); err != nil {
		return err
	}
	return run(tool, "check")
}
func download(ctx context.Context, raw string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 10 || r.URL.Scheme != "https" {
			return fmt.Errorf("unsafe release redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("release asset too large")
	}
	return data, err
}
func upgrade(t Task, verify func() error) error {
	work, err := os.MkdirTemp(Directory, "release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	base := "https://github.com/M1saka10010/SwallowAgent/releases/download/" + url.PathEscape(t.TargetVersion) + "/"
	sums, err := download(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return err
	}
	binary := "swallow-agent-linux-" + runtime.GOARCH
	hashes := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == binary || f[1] == "upgrade.sh") {
			if _, ok := hashes[f[1]]; ok {
				return fmt.Errorf("duplicate checksum")
			}
			decoded, e := hex.DecodeString(f[0])
			if e != nil || len(decoded) != 32 {
				return fmt.Errorf("invalid checksum")
			}
			hashes[f[1]] = strings.ToLower(f[0])
		}
	}
	for _, name := range []string{binary, "upgrade.sh"} {
		if hashes[name] == "" {
			return fmt.Errorf("missing checksum")
		}
		limit := int64(128 << 20)
		if name == "upgrade.sh" {
			limit = 1 << 20
		}
		data, e := download(ctx, base+name, limit)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != hashes[name] {
			return fmt.Errorf("release checksum mismatch")
		}
		if e = atomicWrite(filepath.Join(work, name), data); e != nil {
			return e
		}
	}
	candidate := filepath.Join(work, binary)
	if err = os.Chmod(candidate, 0700); err != nil {
		return err
	}
	out, err := command(candidate, "-maintenance-capable").Output()
	if err != nil || strings.TrimSpace(string(out)) != "maintenance-v1" {
		return fmt.Errorf("target release lacks durable maintenance support")
	}
	if err = run("/usr/bin/bash", filepath.Join(work, "upgrade.sh"), "--binary", candidate, "--version", t.TargetVersion, "--sha256", hashes[binary]); err != nil {
		return err
	}
	if err = verify(); err != nil {
		return err
	}
	if err = run("/usr/bin/systemctl", "is-active", "--quiet", "swallow-agent.service"); err != nil {
		return err
	}
	out, err = command("/usr/bin/systemctl", "show", "--property=MainPID", "--value", "swallow-agent.service").Output()
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("missing running agent PID")
	}
	out, err = command(fmt.Sprintf("/proc/%d/exe", pid), "-version").Output()
	if err != nil || strings.TrimSpace(string(out)) != t.TargetVersion {
		return fmt.Errorf("running agent version verification failed")
	}
	return nil
}
