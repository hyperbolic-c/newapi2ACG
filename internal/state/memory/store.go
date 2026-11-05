// Package memory is an in-process state.Store with the same semantics as the
// PostgreSQL store, used by tests of the sync mechanisms.
package memory

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

type Store struct {
	mu          sync.Mutex
	clock       func() time.Time
	tasks       map[string]state.Task
	checkpoints map[string]state.Checkpoint
	entries     map[string]map[state.MapKey]state.MapEntry
	failCommits int
	commits     int
}

var ErrInjected = errors.New("memory store: injected commit failure")

func New() *Store {
	return &Store{clock: time.Now, tasks: map[string]state.Task{}, checkpoints: map[string]state.Checkpoint{},
		entries: map[string]map[state.MapKey]state.MapEntry{}}
}

// SetClock replaces the clock used for lease expiry (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = now
}

// FailNextCommits makes the next n Commit calls fail before writing,
// simulating a state database outage after the target accepted writes.
func (s *Store) FailNextCommits(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCommits = n
}

func (s *Store) EnsureTask(_ context.Context, id, source, hash string) (state.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		t = state.Task{ID: id, SourceID: source, Phase: state.Initializing, ConfigHash: hash, UpdatedAt: s.clock()}
		s.tasks[id] = t
	}
	return t, nil
}

func (s *Store) LoadTask(_ context.Context, id string) (state.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return state.Task{}, state.ErrTaskNotFound
	}
	return t, nil
}

func (s *Store) AcquireLease(_ context.Context, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false, state.ErrTaskNotFound
	}
	now := s.clock()
	if t.LeaseOwner != "" && t.LeaseOwner != owner && t.LeaseUntil.After(now) {
		return false, nil
	}
	t.LeaseOwner, t.LeaseUntil = owner, now.Add(ttl)
	s.tasks[id] = t
	return true, nil
}

// holds must be called with mu held.
func (s *Store) holds(l state.Lease) (state.Task, error) {
	t, ok := s.tasks[l.TaskID]
	if !ok {
		return t, state.ErrTaskNotFound
	}
	if t.LeaseOwner != l.Owner || !t.LeaseUntil.After(s.clock()) {
		return t, state.ErrLeaseLost
	}
	return t, nil
}

func (s *Store) RenewLease(_ context.Context, l state.Lease, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.holds(l)
	if err != nil {
		return err
	}
	t.LeaseUntil = s.clock().Add(ttl)
	s.tasks[l.TaskID] = t
	return nil
}

func (s *Store) ReleaseLease(_ context.Context, l state.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[l.TaskID]
	if ok && t.LeaseOwner == l.Owner {
		t.LeaseOwner, t.LeaseUntil = "", time.Time{}
		s.tasks[l.TaskID] = t
	}
	return nil
}

func (s *Store) SetPhase(_ context.Context, l state.Lease, phase state.Phase, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.holds(l)
	if err != nil {
		return err
	}
	t.Phase, t.LastError, t.UpdatedAt = phase, lastErr, s.clock()
	s.tasks[l.TaskID] = t
	return nil
}

func (s *Store) SetConfigHash(_ context.Context, l state.Lease, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.holds(l)
	if err != nil {
		return err
	}
	t.ConfigHash = hash
	s.tasks[l.TaskID] = t
	return nil
}

func (s *Store) SetPhaseAdmin(_ context.Context, id string, phase state.Phase, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return state.ErrTaskNotFound
	}
	t.Phase, t.LastError, t.UpdatedAt = phase, lastErr, s.clock()
	s.tasks[id] = t
	return nil
}

func (s *Store) LoadCheckpoint(_ context.Context, id string) (state.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpoints[id], nil
}

func (s *Store) Commit(_ context.Context, l state.Lease, c state.Commit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCommits > 0 {
		s.failCommits--
		return ErrInjected
	}
	if _, err := s.holds(l); err != nil {
		return err
	}
	m := s.entries[l.TaskID]
	if m == nil {
		m = map[state.MapKey]state.MapEntry{}
		s.entries[l.TaskID] = m
	}
	for _, e := range c.Upserts {
		old := m[e.MapKey]
		if e.TargetID == "" {
			e.TargetID = old.TargetID
		}
		if e.LastSeenRun == "" {
			e.LastSeenRun = old.LastSeenRun
		}
		m[e.MapKey] = e
	}
	for _, k := range c.Deletes {
		delete(m, k)
	}
	if c.Checkpoint != nil {
		s.checkpoints[l.TaskID] = *c.Checkpoint
	}
	s.commits++
	return nil
}

func (s *Store) Hashes(_ context.Context, id string, ids []target.TargetIdentity) (map[target.TargetIdentity]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[target.TargetIdentity]string{}
	for k, e := range s.entries[id] {
		if slices.Contains(ids, k.Identity) {
			out[k.Identity] = e.Hash
		}
	}
	return out, nil
}

func (s *Store) Unseen(_ context.Context, id, run string, datasets []string) ([]state.MapEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []state.MapEntry
	for _, e := range s.entries[id] {
		if e.LastSeenRun != run && slices.Contains(datasets, e.Source.Dataset) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b state.MapEntry) int {
		if a.Source.Dataset != b.Source.Dataset {
			return compare(a.Source.Dataset, b.Source.Dataset)
		}
		return compare(a.Source.Key, b.Source.Key)
	})
	return out, nil
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Entries returns a copy of the task's resource map (tests only).
func (s *Store) Entries(id string) map[state.MapKey]state.MapEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[state.MapKey]state.MapEntry{}
	for k, v := range s.entries[id] {
		out[k] = v
	}
	return out
}

// Commits counts successful commits (tests only).
func (s *Store) Commits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}
