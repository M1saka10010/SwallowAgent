package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func simulated(t *testing.T) (*Manager, *int) {
	t.Helper()
	calls := 0
	m := &Manager{dir: t.TempDir(), supported: true, alive: func(Task) (bool, error) { return true, nil }}
	m.launch = func(task Task) error {
		calls++
		s, e := readState(m.dir)
		if e != nil || len(s.Records) == 0 || s.Records[len(s.Records)-1].Events[0].Status != "accepted" {
			t.Fatal("execution before durable acceptance")
		}
		return nil
	}
	return m, &calls
}
func TestReplayDedupAckRestart(t *testing.T) {
	m, calls := simulated(t)
	task := Task{ID: "one", Kind: "system_update"}
	if e := m.apply(Reply{Task: &task}); e != nil {
		t.Fatal(e)
	}
	if e := m.apply(Reply{Task: &task}); e != nil {
		t.Fatal(e)
	}
	if *calls != 1 {
		t.Fatal("duplicate execution")
	}
	for _, status := range []string{"running", "verifying", "succeeded"} {
		if e := transition(m.dir, task.ID, status, status); e != nil {
			t.Fatal(e)
		}
	}
	b, e := m.Batch()
	if e != nil || len(b.Events) != 4 {
		t.Fatalf("batch %+v %v", b, e)
	}
	if e = m.apply(Reply{Acks: []Ack{{task.ID, 1}, {task.ID, 2}}}); e != nil {
		t.Fatal(e)
	}
	restarted := *m
	b, e = restarted.Batch()
	if e != nil || len(b.Events) != 2 || b.Events[0].Seq != 3 {
		t.Fatal(b, e)
	}
	if e = restarted.apply(Reply{Task: &task}); e != nil {
		t.Fatal(e)
	}
	if *calls != 1 {
		t.Fatal("restart duplicate")
	}
}
func TestInterruptedBlocksNewTask(t *testing.T) {
	m, calls := simulated(t)
	first := Task{ID: "one", Kind: "system_update"}
	if e := m.apply(Reply{Task: &first}); e != nil {
		t.Fatal(e)
	}
	m.alive = func(Task) (bool, error) { return false, nil }
	b, e := m.Batch()
	if e != nil || b.Events[1].Status != "unknown" {
		t.Fatal(b, e)
	}
	next := Task{ID: "two", Kind: "system_update"}
	if e = m.apply(Reply{Task: &next}); e != nil {
		t.Fatal(e)
	}
	if *calls != 1 {
		t.Fatal("unknown did not block")
	}
}
func TestLaunchUncertainAndWriteFailure(t *testing.T) {
	m, _ := simulated(t)
	m.launch = func(Task) error { return fmt.Errorf("uncertain") }
	task := Task{ID: "one", Kind: "system_update"}
	if e := m.apply(Reply{Task: &task}); e != nil {
		t.Fatal(e)
	}
	b, e := m.Batch()
	if e != nil || b.Events[1].Status != "unknown" {
		t.Fatal(b, e)
	}
	broken, calls := simulated(t)
	if e = os.Mkdir(filepath.Join(broken.dir, "state.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if broken.apply(Reply{Task: &task}) == nil || *calls != 0 {
		t.Fatal("executed with persistence failure")
	}
}
func TestBoundedBatchAndMessages(t *testing.T) {
	m, _ := simulated(t)
	for i := 0; i < 5; i++ {
		task := Task{ID: fmt.Sprint(i), Kind: "system_update"}
		if e := m.apply(Reply{Task: &task}); e != nil {
			t.Fatal(e)
		}
		for _, status := range []string{"running", "verifying", "succeeded"} {
			if e := transition(m.dir, task.ID, status, strings.Repeat("界", 1000)); e != nil {
				t.Fatal(e)
			}
		}
	}
	b, e := m.Batch()
	if e != nil || len(b.Events) != 16 {
		t.Fatal(b, e)
	}
	for _, event := range b.Events {
		if len(event.Message) > 2048 || !utf8.ValidString(event.Message) {
			t.Fatal("unbounded/invalid message")
		}
	}
}
func TestRejectRemoteInputs(t *testing.T) {
	for _, task := range []Task{{ID: "../x", Kind: "system_update"}, {ID: "x", Kind: "shell"}, {ID: "x", Kind: "agent_upgrade", TargetVersion: "v1.2.3;id"}, {ID: "x", Kind: "system_update", TargetVersion: "https://example.invalid"}} {
		if valid(task) {
			t.Fatal(task)
		}
	}
}
func TestFinalPersistenceRetryIdempotent(t *testing.T) {
	m, _ := simulated(t)
	task := Task{ID: "one", Kind: "system_update"}
	if e := m.apply(Reply{Task: &task}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := transition(m.dir, task.ID, "succeeded", "done"); e != nil {
			t.Fatal(e)
		}
	}
	s, e := readState(m.dir)
	if e != nil || len(s.Records[0].Events) != 2 {
		t.Fatal(s, e)
	}
}

func TestDurableOutputLimitAndFinal(t *testing.T) {
	m, _ := simulated(t)
	task := Task{ID: "output", Kind: "system_update"}
	if err := m.apply(Reply{Task: &task}); err != nil {
		t.Fatal(err)
	}
	w := durableOutput{dir: m.dir, id: task.ID}
	if _, err := w.Write([]byte(strings.Repeat("x", 100000))); err != nil {
		t.Fatal(err)
	}
	if err := transition(m.dir, task.ID, "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	s, err := readState(m.dir)
	if err != nil {
		t.Fatal(err)
	}
	events := s.Records[0].Events
	if len(events) != 34 || !strings.Contains(events[32].Message, "truncated") || events[33].Status != "succeeded" {
		t.Fatal(events)
	}
	if err = m.apply(Reply{Acks: []Ack{{task.ID, 16}}}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Batch()
	if err != nil || b.Events[0].Seq != 17 {
		t.Fatal(b, err)
	}
}

func TestDurableOutputPreservesLifecyclePhase(t *testing.T) {
	m, _ := simulated(t)
	task := Task{ID: "phase", Kind: "system_update"}
	if err := m.apply(Reply{Task: &task}); err != nil {
		t.Fatal(err)
	}
	if err := transition(m.dir, task.ID, "running", "started"); err != nil {
		t.Fatal(err)
	}
	if err := transition(m.dir, task.ID, "verifying", "checking"); err != nil {
		t.Fatal(err)
	}
	w := durableOutput{dir: m.dir, id: task.ID}
	if _, err := w.Write([]byte("package check output")); err != nil {
		t.Fatal(err)
	}
	s, err := readState(m.dir)
	if err != nil {
		t.Fatal(err)
	}
	last := s.Records[0].Events[len(s.Records[0].Events)-1]
	if last.Status != "verifying" || last.Message != "package check output" {
		t.Fatalf("unexpected output event: %+v", last)
	}
}

func TestDurableOutputSkipsStateAfterLimit(t *testing.T) {
	m, _ := simulated(t)
	task := Task{ID: "cap", Kind: "system_update"}
	if err := m.apply(Reply{Task: &task}); err != nil {
		t.Fatal(err)
	}
	w := durableOutput{dir: m.dir, id: task.ID}
	if _, err := w.Write([]byte(strings.Repeat("x", 100000))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("ignored")); err != nil || n != len("ignored") {
		t.Fatalf("post-limit write = %d, %v", n, err)
	}
}

func TestProbeFailureStillReplays(t *testing.T) {
	m, _ := simulated(t)
	task := Task{ID: "probe", Kind: "system_update"}
	if err := m.apply(Reply{Task: &task}); err != nil {
		t.Fatal(err)
	}
	m.alive = func(Task) (bool, error) { return false, fmt.Errorf("unavailable") }
	b, err := m.Batch()
	if err != nil || len(b.Events) != 2 || b.Events[1].Status != "unknown" {
		t.Fatal(b, err)
	}
	if err = transition(m.dir, task.ID, "succeeded", "observed by surviving worker"); err != nil {
		t.Fatal(err)
	}
}
