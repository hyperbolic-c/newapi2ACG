package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/service"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/pg"
)

const testDSN = "postgres://migrator:dsn-secret-value@db/state"

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fakeBackend struct {
	mu      sync.Mutex
	states  map[string]service.JobState
	saved   chan service.JobState
	closed  bool
	ensured []string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{states: map[string]service.JobState{}, saved: make(chan service.JobState, 16)}
}
func (b *fakeBackend) Ensure(_ context.Context, job string, _ any, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensured = append(b.ensured, job)
	if _, ok := b.states[job]; !ok {
		b.states[job] = service.JobState{State: service.Precheck}
	}
	return nil
}
func (b *fakeBackend) Load(_ context.Context, job string) (service.JobState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.states[job], nil
}
func (b *fakeBackend) Save(_ context.Context, job string, s service.JobState) error {
	b.mu.Lock()
	b.states[job] = s
	b.mu.Unlock()
	select {
	case b.saved <- s:
	default:
	}
	return nil
}
func (b *fakeBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}
func (b *fakeBackend) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

type fakeLease struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	closed bool
}

func newFakeLease(parent context.Context) *fakeLease {
	ctx, cancel := context.WithCancelCause(parent)
	return &fakeLease{ctx: ctx, cancel: cancel}
}
func (l *fakeLease) Context() context.Context { return l.ctx }
func (l *fakeLease) Close() error {
	l.cancel(pg.ErrLeaseClosed)
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}
func (l *fakeLease) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

type harness struct {
	backend  *fakeBackend
	lease    *fakeLease
	attempts int
	heldFor  int
	opened   bool
	mu       sync.Mutex
}

func (h *harness) env(t *testing.T) runEnv {
	return runEnv{
		getenv: func(name string) string {
			if name != "MIGRATOR_STATE_DSN" {
				t.Errorf("unexpected env lookup %s", name)
			}
			return testDSN
		},
		openState: func(_ context.Context, dsn string) (stateBackend, error) {
			if dsn != testDSN {
				t.Error("wrong DSN passed to state store")
			}
			h.opened = true
			return h.backend, nil
		},
		acquire: func(ctx context.Context, _, _ string) (jobLease, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.attempts++
			if h.attempts <= h.heldFor {
				return nil, pg.ErrLeaseHeld
			}
			h.lease = newFakeLease(ctx)
			return h.lease, nil
		},
	}
}
func (h *harness) currentLease() *fakeLease {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lease
}

func startRun(t *testing.T, h *harness, extra ...string) (context.CancelFunc, <-chan int, *syncBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuffer{}
	done := make(chan int, 1)
	args := append([]string{"--config-dir", "../../configs", "--lease-retry", "1ms", "--work-retry", "1h"}, extra...)
	go func() { done <- runCommand(ctx, args, logs, h.env(t)) }()
	return cancel, done, logs
}
func waitExit(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit")
		return -1
	}
}

func TestRunEmptyJobStaysInPrecheck(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	cancel, done, logs := startRun(t, h)
	select {
	case s := <-h.backend.saved:
		if s.State != service.Precheck || s.Reason != precheckPending {
			t.Fatalf("unexpected persisted state %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("precheck outcome not persisted")
	}
	cancel()
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("graceful shutdown exit %d: %s", code, logs)
	}
	if !h.currentLease().isClosed() || !h.backend.isClosed() {
		t.Fatal("lease or state store not released on shutdown")
	}
	if strings.Contains(logs.String(), "dsn-secret-value") {
		t.Fatal("DSN leaked into logs")
	}
	if len(h.backend.ensured) != 1 || h.backend.ensured[0] != "example" {
		t.Fatalf("job not registered: %v", h.backend.ensured)
	}
}
func TestRunWaitsForHeldLease(t *testing.T) {
	h := &harness{backend: newFakeBackend(), heldFor: 3}
	cancel, done, logs := startRun(t, h)
	select {
	case <-h.backend.saved:
	case <-time.After(2 * time.Second):
		t.Fatal("did not start after lease became free")
	}
	cancel()
	waitExit(t, done)
	if h.attempts != 4 || !strings.Contains(logs.String(), "held by another instance") {
		t.Fatalf("attempts=%d logs=%s", h.attempts, logs)
	}
}
func TestRunShutdownWhileWaitingForLease(t *testing.T) {
	h := &harness{backend: newFakeBackend(), heldFor: 1 << 30}
	cancel, done, _ := startRun(t, h)
	time.Sleep(20 * time.Millisecond)
	cancel()
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !h.backend.isClosed() {
		t.Fatal("state store not closed")
	}
}
func TestRunLeaseLossExitsNonZero(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	_, done, logs := startRun(t, h)
	select {
	case <-h.backend.saved:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}
	h.currentLease().cancel(pg.ErrLeaseLost)
	if code := waitExit(t, done); code != 1 || !strings.Contains(logs.String(), "lease lost") {
		t.Fatalf("exit %d logs %s", code, logs)
	}
}
func TestRunPausedJobIdlesWithoutWork(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	h.backend.states["example"] = service.JobState{State: service.Paused, ResumeFrom: service.Streaming}
	cancel, done, _ := startRun(t, h)
	select {
	case code := <-done:
		t.Fatalf("paused job exited early with %d", code)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, _ := h.backend.Load(context.Background(), "example"); got.State != service.Paused {
		t.Fatalf("paused job modified: %+v", got)
	}
}
func TestRunPersistedStateWithoutHandlerFails(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	h.backend.states["example"] = service.JobState{State: service.Streaming}
	_, done, logs := startRun(t, h)
	if code := waitExit(t, done); code != 1 || !strings.Contains(logs.String(), "no handler registered") {
		t.Fatalf("exit %d logs %s", code, logs)
	}
}
func TestRunRequiresStateDSN(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	env := h.env(t)
	env.getenv = func(string) string { return "" }
	var errs bytes.Buffer
	if code := runCommand(context.Background(), []string{"--config-dir", "../../configs"}, &errs, env); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errs.String(), "MIGRATOR_STATE_DSN is not set") || h.opened {
		t.Fatalf("missing DSN not reported before connecting: %s", &errs)
	}
}
func TestRunUnknownJob(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	var errs bytes.Buffer
	if code := runCommand(context.Background(), []string{"--config-dir", "../../configs", "--job", "absent"}, &errs, h.env(t)); code != 2 || h.opened {
		t.Fatalf("exit %d: %s", code, &errs)
	}
}
func TestRunStateStoreFailureSanitized(t *testing.T) {
	h := &harness{backend: newFakeBackend()}
	env := h.env(t)
	env.openState = func(context.Context, string) (stateBackend, error) {
		return nil, errors.New("connect state database failed")
	}
	var errs bytes.Buffer
	if code := runCommand(context.Background(), []string{"--config-dir", "../../configs"}, &errs, env); code != 1 || strings.Contains(errs.String(), "dsn-secret-value") {
		t.Fatalf("exit %d: %s", code, &errs)
	}
}
