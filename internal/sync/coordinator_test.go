package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	gosync "sync"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/mapping"
	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/memory"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
	"github.com/hyperbolic-c/newapi2ACG/internal/target/mock"
)

type syncBuffer struct {
	mu gosync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type harness struct {
	t      *testing.T
	src    *fakeSource
	target *mock.Writer
	store  *memory.Store
	logs   *syncBuffer
	coord  *Coordinator
	cancel context.CancelFunc
	done   chan error
}

func newHarness(t *testing.T, edit func(*Coordinator)) *harness {
	t.Helper()
	t.Setenv("TEST_HASH_KEY", "test-key")
	hasher, err := secrets.NewFingerprinter("TEST_HASH_KEY")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, src: newFakeSource(), target: mock.New(), store: memory.New(), logs: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	metrics := observability.NewMetrics()
	retry := Retry{Attempts: 3, Base: time.Millisecond, Max: 5 * time.Millisecond, Log: log, Metrics: metrics}
	h.coord = &Coordinator{
		TaskID: "task", SourceID: "src", ConfigHash: "hash-1", Owner: "owner-a",
		Source: h.src, Streamer: h.src, Store: h.store, Datasets: []string{"users"},
		Pipeline: &Pipeline{TaskID: "task", Mapper: mapping.Passthrough{}, Target: h.target, Store: h.store,
			Hasher: hasher, Chunk: 2, Retry: retry, Log: log, Metrics: metrics},
		Full:            config.Full{Enabled: true, OnFirstRun: true, BatchSize: 2},
		Incremental:     config.Incremental{Enabled: true},
		Reconcile:       config.Reconcile{Enabled: false, Interval: config.Duration(time.Hour), BatchSize: 2, DeletePolicy: "report"},
		LeaseTTL:        3 * time.Second,
		Log:             log,
		ControlInterval: 5 * time.Millisecond,
		IdleFlush:       time.Millisecond,
		ErrorBackoff:    time.Millisecond,
	}
	if edit != nil {
		edit(h.coord)
	}
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	go func() { h.done <- h.coord.Run(ctx) }()
	h.t.Cleanup(func() { _ = h.stop() })
}

func (h *harness) stop() error {
	if h.cancel == nil {
		return nil
	}
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("coordinator did not stop")
		return nil
	}
}

func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	task, _ := h.store.LoadTask(context.Background(), "task")
	h.t.Fatalf("timed out waiting for %s (phase=%s last_error=%q target=%v)", what, task.Phase, task.LastError, h.target.Resources())
}

func (h *harness) phase() state.Phase {
	task, _ := h.store.LoadTask(context.Background(), "task")
	return task.Phase
}

func id(key string) target.TargetIdentity { return target.TargetIdentity{Kind: "users", ID: key} }

func row(name string) map[string]any { return map[string]any{"name": name} }

// targetMatchesSource reports whether the mock target equals the source table.
func (h *harness) targetMatchesSource() bool {
	h.src.mu.Lock()
	defer h.src.mu.Unlock()
	res := h.target.Resources()
	if len(res) != len(h.src.rows["users"]) {
		return false
	}
	for k, r := range h.src.rows["users"] {
		got, ok := res[id(k)]
		if !ok || fmt.Sprint(got) != fmt.Sprint(r) {
			return false
		}
	}
	return true
}

func (h *harness) applied() uint64 {
	cp, _ := h.store.LoadCheckpoint(context.Background(), "task")
	return cp.AppliedPosition.BinlogPos
}

func TestFullThenIncremental(t *testing.T) {
	h := newHarness(t, nil)
	for i := 1; i <= 5; i++ {
		h.src.seed(fmt.Sprint(i), row(fmt.Sprint("u", i)))
	}
	h.start()
	h.eventually("full sync and streaming", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
	h.src.commit(upsertRow("6", row("u6")))
	h.src.commit(upsertRow("1", row("renamed")), deleteRow("2")) // one transaction, two changes
	h.eventually("incremental changes", func() bool { return h.targetMatchesSource() && h.applied() == 2 })
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestChangesDuringFullSyncAreCaughtUp(t *testing.T) {
	h := newHarness(t, nil)
	h.src.seed("1", row("a"))
	h.src.seed("2", row("b"))
	once := gosync.Once{}
	h.src.setOnOpen(func() {
		once.Do(func() { // writes after the snapshot started: invisible to it
			h.src.commit(upsertRow("1", row("a2")))
			h.src.commit(upsertRow("3", row("c")))
			h.src.commit(deleteRow("2"))
		})
	})
	h.start()
	h.eventually("catch-up after full sync", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
}

func TestTransientTargetFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	h.target.InjectFaults(mock.FaultFail, mock.FaultFail)
	h.src.commit(upsertRow("1", row("a")), upsertRow("2", row("b")), upsertRow("3", row("c")))
	h.eventually("transaction applied after retries", func() bool { return h.targetMatchesSource() && h.applied() == 1 })
}

func TestLostResponseIsConfirmedNotRepeated(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	h.target.InjectFaults(mock.FaultLostResponse)
	h.src.commit(upsertRow("1", row("a")))
	h.eventually("applied", func() bool { return h.targetMatchesSource() && h.applied() == 1 })
	if n := h.target.AppliedOperations(); n != 1 {
		t.Fatalf("operation applied %d times; a confirmed write must not be repeated", n)
	}
}

func TestStateCommitFailureReplaysWithoutDuplicates(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	h.store.FailNextCommits(10) // exceeds the pipeline's retries: the batch is replayed
	h.src.commit(upsertRow("1", row("a")))
	h.eventually("applied after replay", func() bool { return h.applied() == 1 })
	if len(h.target.Resources()) != 1 || !h.targetMatchesSource() {
		t.Fatalf("replay created duplicates or lost data: %v", h.target.Resources())
	}
}

func TestPermanentTargetErrorStopsTask(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	h.target.InjectFaults(mock.FaultPermanent)
	h.src.commit(upsertRow("1", row("a")))
	h.eventually("failed", func() bool { return h.phase() == state.Failed })
	if h.applied() != 0 {
		t.Fatal("checkpoint advanced past a failed transaction")
	}
	task, _ := h.store.LoadTask(context.Background(), "task")
	if task.LastError == "" {
		t.Fatal("failure reason not recorded")
	}
	// Operator recovery resumes from the unchanged checkpoint.
	if err := h.store.SetPhaseAdmin(context.Background(), "task", state.Initializing, ""); err != nil {
		t.Fatal(err)
	}
	h.eventually("recovered", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() && h.applied() == 1 })
}

func TestStreamDisconnectResumesFromCheckpoint(t *testing.T) {
	h := newHarness(t, nil)
	h.src.setStreamErrors(errors.New("connection reset"), errors.New("connection reset"))
	h.start()
	h.src.commit(upsertRow("1", row("a")))
	h.src.commit(upsertRow("2", row("b")))
	h.eventually("resumed", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() && h.applied() == 2 })
	h.eventually("reconnected after each failure", func() bool { _, streams := h.src.counts(); return streams >= 3 })
}

func TestPositionLostStartsNewFullSync(t *testing.T) {
	h := newHarness(t, nil)
	h.src.seed("1", row("a"))
	h.src.setStreamErrors(fmt.Errorf("%w (test)", source.ErrPositionLost))
	h.start()
	h.eventually("second snapshot", func() bool { snapshots, _ := h.src.counts(); return snapshots >= 2 })
	h.src.commit(upsertRow("2", row("b")))
	h.eventually("streaming again", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
}

func TestIncompatibleDDLStopsWithoutAdvancing(t *testing.T) {
	h := newHarness(t, nil)
	h.src.setStreamErrors(fmt.Errorf("%w: ALTER TABLE users DROP COLUMN name", source.ErrIncompatibleDDL))
	h.start()
	h.eventually("failed", func() bool { return h.phase() == state.Failed })
	task, _ := h.store.LoadTask(context.Background(), "task")
	if h.applied() != 0 || task.LastError == "" {
		t.Fatalf("applied=%d last_error=%q", h.applied(), task.LastError)
	}
}

func TestPauseAndResume(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	if err := h.store.SetPhaseAdmin(context.Background(), "task", state.Paused, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond) // let the coordinator notice
	h.src.commit(upsertRow("1", row("a")))
	time.Sleep(30 * time.Millisecond)
	if len(h.target.Resources()) != 0 || h.phase() != state.Paused {
		t.Fatal("paused task applied changes")
	}
	if err := h.store.SetPhaseAdmin(context.Background(), "task", state.Initializing, ""); err != nil {
		t.Fatal(err)
	}
	h.eventually("resumed", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
}

func reconcileHarness(t *testing.T, policy string) *harness {
	return newHarness(t, func(c *Coordinator) {
		c.Reconcile = config.Reconcile{Enabled: true, Interval: config.Duration(time.Hour), BatchSize: 2, DeletePolicy: policy}
	})
}

func TestReconcileRepairsDriftAndDeletesOrphans(t *testing.T) {
	h := reconcileHarness(t, "delete")
	for i := 1; i <= 3; i++ {
		h.src.seed(fmt.Sprint(i), row(fmt.Sprint("u", i)))
	}
	h.start()
	h.eventually("first full sync", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	// While stopped: someone edits ACG by hand, and a source row disappears
	// without a binlog event reaching us.
	h.target.Set(id("1"), row("edited-in-acg"))
	h.src.mu.Lock()
	delete(h.src.rows["users"], "3")
	h.src.mu.Unlock()
	// A configuration change triggers a reconciliation on the next start.
	h.coord.ConfigHash = "hash-2"
	h.start()
	h.eventually("reconciled", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
	if _, ok := h.store.Entries("task")[state.MapKey{Source: target.SourceRef{Dataset: "users", Key: "3"}, Identity: id("3")}]; ok {
		t.Fatal("resource map still lists the deleted row")
	}
}

func TestReconcileReportPolicyKeepsOrphans(t *testing.T) {
	h := reconcileHarness(t, "report")
	h.src.seed("1", row("a"))
	h.src.seed("2", row("b"))
	h.start()
	h.eventually("first full sync", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
	_ = h.stop()
	h.src.mu.Lock()
	delete(h.src.rows["users"], "2")
	h.src.mu.Unlock()
	h.coord.ConfigHash = "hash-2"
	h.start()
	h.eventually("reconciled", func() bool {
		cp, _ := h.store.LoadCheckpoint(context.Background(), "task")
		return len(cp.ReconcileRunID) > 10 && cp.ReconcileRunID[:9] == "reconcile" && h.phase() == state.Streaming
	})
	if _, ok := h.target.Resources()[id("2")]; !ok {
		t.Fatal("report policy deleted a resource")
	}
}

func TestReconcileDoesNotOverwriteNewerChanges(t *testing.T) {
	h := reconcileHarness(t, "report")
	h.src.seed("1", row("v1"))
	h.start()
	h.eventually("first full sync", func() bool { return h.phase() == state.Streaming && h.targetMatchesSource() })
	_ = h.stop()
	// During the reconciliation snapshot, the row changes again. The
	// snapshot shows v1; the change arrives through the binlog after P1.
	var once gosync.Once
	h.src.setOnOpen(func() { once.Do(func() { h.src.commit(upsertRow("1", row("v2"))) }) })
	h.coord.ConfigHash = "hash-2"
	h.start()
	h.eventually("latest value wins", func() bool {
		return h.phase() == state.Streaming && h.target.Resources()[id("1")]["name"] == "v2"
	})
}

func TestSecondInstanceWaitsForLease(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	other := *h.coord
	other.Owner = "owner-b"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := other.Run(ctx); err != nil {
		t.Fatalf("waiting instance returned %v", err)
	}
	task, _ := h.store.LoadTask(context.Background(), "task")
	if task.LeaseOwner != "owner-a" {
		t.Fatalf("lease moved to %s while the owner was alive", task.LeaseOwner)
	}
}

func TestLeaseLossStopsRun(t *testing.T) {
	h := newHarness(t, func(c *Coordinator) { c.LeaseTTL = 300 * time.Millisecond })
	h.start()
	h.eventually("streaming", func() bool { return h.phase() == state.Streaming })
	// Another instance takes over (for example after a network partition).
	now := time.Now()
	h.store.SetClock(func() time.Time { return now.Add(time.Hour) })
	if ok, _ := h.store.AcquireLease(context.Background(), "task", "owner-b", time.Hour); !ok {
		t.Fatal("takeover failed")
	}
	select {
	case err := <-h.done:
		if !errors.Is(err, state.ErrLeaseLost) {
			t.Fatalf("run returned %v", err)
		}
		h.cancel = nil
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after losing the lease")
	}
}

func TestSecretsNeverReachLogs(t *testing.T) {
	h := newHarness(t, nil)
	h.src.seed("1", map[string]any{"name": "a", "key": secrets.New("sk-very-secret")})
	h.target.InjectFaults(mock.FaultFail, mock.FaultLostResponse)
	h.start()
	h.eventually("synced", func() bool { return h.phase() == state.Streaming && len(h.target.Resources()) == 1 })
	if bytes.Contains([]byte(h.logs.String()), []byte("sk-very-secret")) {
		t.Fatal("secret value written to logs")
	}
}
