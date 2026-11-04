package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test repository represents persistence across runner instances. Real PG
// durability is a separate integration gate, intentionally not claimed here.
type testRepository struct {
	mu    sync.Mutex
	state JobState
	saved chan JobState
}

func (s *testRepository) Load(_ context.Context, _ string) (JobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}
func (s *testRepository) Save(_ context.Context, _ string, state JobState) error {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	if s.saved != nil {
		select {
		case s.saved <- state:
		default:
		}
	}
	return nil
}

type testLease struct{ ctx context.Context }

func (l testLease) Context() context.Context { return l.ctx }

func TestResumeFromPersistedState(t *testing.T) {
	repo := &testRepository{state: JobState{State: Streaming}}
	calls := make(chan State, 1)
	runner := Runner{Store: repo, Handlers: map[State]Handler{Streaming: func(ctx context.Context) (Event, error) { calls <- Streaming; return Stop, nil }}}
	if err := runner.Run(context.Background(), "job", testLease{context.Background()}); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-calls:
		if state != Streaming {
			t.Fatal("wrong resumed state")
		}
	default:
		t.Fatal("did not resume streaming")
	}
	got, _ := repo.Load(context.Background(), "job")
	if got.State != Stopped {
		t.Fatal("stop not saved")
	}
}
func TestPrecheckFailureStaysInPrecheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &testRepository{state: JobState{State: Precheck}, saved: make(chan JobState, 1)}
	runner := Runner{Store: repo, RetryDelay: time.Hour, Handlers: map[State]Handler{Precheck: func(context.Context) (Event, error) { return "", errors.New("sensitive-error-text") }}}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, "job", testLease{ctx}) }()
	select {
	case state := <-repo.saved:
		if state.State != Precheck || state.Reason == "" || state.Reason == "sensitive-error-text" {
			t.Fatalf("invalid failure state: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("precheck failure not persisted")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected exit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner failed to exit")
	}
}
func TestLeaseLossStopsRunner(t *testing.T) {
	leaseCtx, lose := context.WithCancel(context.Background())
	defer lose()
	started := make(chan struct{})
	done := make(chan error, 1)
	repo := &testRepository{state: JobState{State: Streaming}}
	runner := Runner{Store: repo, Handlers: map[State]Handler{Streaming: func(ctx context.Context) (Event, error) { close(started); <-ctx.Done(); return "", ctx.Err() }}}
	go func() { done <- runner.Run(context.Background(), "job", testLease{leaseCtx}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	lose()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected exit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel handler")
	}
}
func TestPausedJobDoesNotExecuteHandlers(t *testing.T) {
	repo := &testRepository{state: JobState{State: Paused, ResumeFrom: Streaming}}
	runner := Runner{Store: repo, Handlers: map[State]Handler{Paused: func(context.Context) (Event, error) { t.Fatal("paused job executed work"); return Stop, nil }}}
	if err := runner.Run(context.Background(), "job", testLease{context.Background()}); err != nil {
		t.Fatal(err)
	}
}
func TestReasonErrorPersistedOnlyWhenExplicit(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ReasonError{Reason: "precheck not implemented"}, "precheck not implemented"},
		{fmt.Errorf("wrapped: %w", &ReasonError{Reason: "safe summary"}), "safe summary"},
		{errors.New("dsn=postgres://user:secret@host"), "PRECHECK work failed"},
		{&ReasonError{Reason: strings.Repeat("x", MaxReasonLength+1)}, "PRECHECK work failed"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		repo := &testRepository{state: JobState{State: Precheck}, saved: make(chan JobState, 1)}
		runner := Runner{Store: repo, RetryDelay: time.Hour, Handlers: map[State]Handler{Precheck: func(context.Context) (Event, error) { return "", tc.err }}}
		done := make(chan error, 1)
		go func() { done <- runner.Run(ctx, "job", testLease{ctx}) }()
		select {
		case got := <-repo.saved:
			if got.Reason != tc.want {
				t.Errorf("reason %q, want %q", got.Reason, tc.want)
			}
		case <-time.After(time.Second):
			t.Fatal("failure not persisted")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected exit: %v", err)
		}
	}
}
