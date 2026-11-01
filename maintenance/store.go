package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"
)

const Directory = "/opt/swallow-agent/maintenance"

type Task struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	TargetVersion string `json:"targetVersion"`
	CreatedAt     int64  `json:"createdAt"`
}
type Event struct {
	TaskID    string `json:"taskId"`
	Seq       int    `json:"seq"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"createdAt"`
}
type Ack struct {
	TaskID string `json:"taskId"`
	Seq    int    `json:"seq"`
}
type Reply struct {
	Acks []Ack `json:"acks"`
	Task *Task `json:"task,omitempty"`
}
type Batch struct {
	Supported bool    `json:"supported"`
	Events    []Event `json:"events"`
}
type record struct {
	Task   Task
	Events []Event
	Ack    int
}
type state struct{ Records []record }

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var version = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\+[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*)?$`)

func valid(t Task) bool {
	return identifier.MatchString(t.ID) && (t.Kind == "system_update" && t.TargetVersion == "" || t.Kind == "agent_upgrade" && len(t.TargetVersion) <= 64 && version.MatchString(t.TargetVersion))
}
func terminal(s string) bool { return s == "succeeded" || s == "failed" }
func appendEvent(r *record, status, message string) {
	if len(message) > 2048 {
		message = message[:2048]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	r.Events = append(r.Events, Event{r.Task.ID, len(r.Events) + 1, status, message, time.Now().Unix()})
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func readState(dir string) (state, error) {
	var s state
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	if len(s.Records) > 4096 {
		return s, fmt.Errorf("maintenance journal exceeds limit")
	}
	seen := map[string]bool{}
	for _, r := range s.Records {
		if !valid(r.Task) || seen[r.Task.ID] || len(r.Events) == 0 || len(r.Events) > 40 || r.Ack < 0 || r.Ack > len(r.Events) {
			return s, fmt.Errorf("invalid maintenance journal")
		}
		seen[r.Task.ID] = true
		for i, e := range r.Events {
			if e.Seq != i+1 || e.TaskID != r.Task.ID || len(e.Message) > 2048 {
				return s, fmt.Errorf("invalid maintenance event")
			}
		}
	}
	return s, nil
}
func writeState(dir string, s state) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "state.json"), data)
}
