package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

func TestLeaseFencing(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.SetClock(func() time.Time { return now })
	ctx := context.Background()
	if _, err := s.EnsureTask(ctx, "t", "src", "h"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AcquireLease(ctx, "t", "a", 30*time.Second); !ok {
		t.Fatal("first acquire failed")
	}
	if ok, _ := s.AcquireLease(ctx, "t", "b", 30*time.Second); ok {
		t.Fatal("second owner acquired a live lease")
	}
	now = now.Add(31 * time.Second)
	if ok, _ := s.AcquireLease(ctx, "t", "b", 30*time.Second); !ok {
		t.Fatal("expired lease not taken over")
	}
	stale := state.Lease{TaskID: "t", Owner: "a"}
	if err := s.Commit(ctx, stale, state.Commit{Checkpoint: &state.Checkpoint{SnapshotDone: true}}); !errors.Is(err, state.ErrLeaseLost) {
		t.Fatalf("stale owner committed: %v", err)
	}
	if err := s.SetPhase(ctx, stale, state.Streaming, ""); !errors.Is(err, state.ErrLeaseLost) {
		t.Fatalf("stale owner set phase: %v", err)
	}
	if err := s.RenewLease(ctx, stale, time.Minute); !errors.Is(err, state.ErrLeaseLost) {
		t.Fatalf("stale owner renewed: %v", err)
	}
}

func TestCommitKeepsUnsetFieldsAndListsUnseen(t *testing.T) {
	s := New()
	ctx := context.Background()
	_, _ = s.EnsureTask(ctx, "t", "src", "h")
	_, _ = s.AcquireLease(ctx, "t", "a", time.Minute)
	l := state.Lease{TaskID: "t", Owner: "a"}
	key := func(k string) state.MapKey {
		return state.MapKey{Source: target.SourceRef{Dataset: "users", Key: k}, Identity: target.TargetIdentity{Kind: "users", ID: k}}
	}
	if err := s.Commit(ctx, l, state.Commit{Upserts: []state.MapEntry{
		{MapKey: key("1"), TargetID: "x1", Hash: "h1", LastSeenRun: "r1"},
		{MapKey: key("2"), TargetID: "x2", Hash: "h2", LastSeenRun: "r1"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, l, state.Commit{Upserts: []state.MapEntry{{MapKey: key("1"), Hash: "h1b", LastSeenRun: "r2"}}}); err != nil {
		t.Fatal(err)
	}
	if e := s.Entries("t")[key("1")]; e.TargetID != "x1" || e.Hash != "h1b" {
		t.Fatalf("entry %+v", e)
	}
	unseen, _ := s.Unseen(ctx, "t", "r2", []string{"users"})
	if len(unseen) != 1 || unseen[0].Source.Key != "2" {
		t.Fatalf("unseen %+v", unseen)
	}
	if other, _ := s.Unseen(ctx, "t", "r2", []string{"tokens"}); len(other) != 0 {
		t.Fatal("unseen must be limited to configured datasets")
	}
}
