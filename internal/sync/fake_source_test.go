package sync

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	gosync "sync"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
)

// fakeSource is a scripted source: tables plus a change log whose position
// is the number of committed transactions.
type fakeSource struct {
	mu        gosync.Mutex
	rows      map[string]map[string]map[string]any // dataset → key → row
	log       [][]event.ChangeEvent
	notify    chan struct{}
	streamErr []error // returned by successive Stream calls, after delivering pending batches
	onOpen    func()  // called after a snapshot is opened
	snapshots int
	streams   int
}

func newFakeSource() *fakeSource {
	return &fakeSource{rows: map[string]map[string]map[string]any{"users": {}}, notify: make(chan struct{}, 1)}
}

func pos(n int) event.SourcePosition {
	return event.SourcePosition{Mode: event.ModeFilePos, BinlogFile: "bin.000001", BinlogPos: uint64(n)}
}

func rec(key string, row map[string]any) *event.Record {
	return &event.Record{Dataset: "users", Key: key, Data: maps.Clone(row)}
}

// seed inserts rows without logging them (state before the task starts).
func (f *fakeSource) seed(key string, row map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows["users"][key] = maps.Clone(row)
}

// commit applies a transaction and appends it to the change log.
func (f *fakeSource) commit(changes ...func(map[string]map[string]any) event.ChangeEvent) {
	f.mu.Lock()
	tx := make([]event.ChangeEvent, 0, len(changes))
	for _, ch := range changes {
		tx = append(tx, ch(f.rows["users"]))
	}
	f.log = append(f.log, tx)
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
}

func upsertRow(key string, row map[string]any) func(map[string]map[string]any) event.ChangeEvent {
	return func(t map[string]map[string]any) event.ChangeEvent {
		before, existed := t[key]
		t[key] = maps.Clone(row)
		if existed {
			return event.ChangeEvent{Operation: event.OpUpdate, Before: rec(key, before), After: rec(key, row)}
		}
		return event.ChangeEvent{Operation: event.OpInsert, After: rec(key, row)}
	}
}

func deleteRow(key string) func(map[string]map[string]any) event.ChangeEvent {
	return func(t map[string]map[string]any) event.ChangeEvent {
		before := t[key]
		delete(t, key)
		return event.ChangeEvent{Operation: event.OpDelete, Before: rec(key, before)}
	}
}

func (f *fakeSource) Check(context.Context) error { return nil }

func (f *fakeSource) counts() (snapshots, streams int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshots, f.streams
}

func (f *fakeSource) setOnOpen(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onOpen = fn
}

func (f *fakeSource) setStreamErrors(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamErr = errs
}

func (f *fakeSource) OpenSnapshot(context.Context) (source.SnapshotSession, error) {
	f.mu.Lock()
	f.snapshots++
	copyRows := map[string]map[string]map[string]any{}
	for ds, t := range f.rows {
		copyRows[ds] = map[string]map[string]any{}
		for k, r := range t {
			copyRows[ds][k] = maps.Clone(r)
		}
	}
	s := &fakeSession{rows: copyRows, pos: pos(len(f.log))}
	hook := f.onOpen
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return s, nil
}

func (f *fakeSource) CurrentPosition(context.Context) (event.SourcePosition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return pos(len(f.log)), nil
}

func (f *fakeSource) Covers(a, t event.SourcePosition) (bool, error) {
	return a.BinlogPos >= t.BinlogPos, nil
}

func (f *fakeSource) Stream(ctx context.Context, from event.SourcePosition, consume func(event.ChangeBatch) error) error {
	f.mu.Lock()
	f.streams++
	var injected error
	if len(f.streamErr) > 0 {
		injected, f.streamErr = f.streamErr[0], f.streamErr[1:]
	}
	f.mu.Unlock()
	next := int(from.BinlogPos)
	for {
		f.mu.Lock()
		var tx []event.ChangeEvent
		have := next < len(f.log)
		if have {
			tx = f.log[next]
		}
		f.mu.Unlock()
		if have {
			end := pos(next + 1)
			events := slices.Clone(tx)
			for i := range events {
				events[i].Position = end
			}
			if err := consume(event.ChangeBatch{Events: events, EndPosition: end}); err != nil {
				return err
			}
			next++
			continue
		}
		if injected != nil {
			return injected
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.notify:
		}
	}
}

type fakeSession struct {
	rows map[string]map[string]map[string]any
	pos  event.SourcePosition
}

func (s *fakeSession) Position() event.SourcePosition { return s.pos }
func (s *fakeSession) Close() error                   { return nil }
func (s *fakeSession) ReadBatch(_ context.Context, ds string, cur event.Cursor, limit int) ([]event.Record, event.Cursor, error) {
	t, ok := s.rows[ds]
	if !ok {
		return nil, cur, errors.New("unknown dataset")
	}
	keys := slices.SortedFunc(maps.Keys(t), func(a, b string) int { return atoi(a) - atoi(b) })
	var out []event.Record
	for _, k := range keys {
		if cur.Started && atoi(k) <= cur.Value.(int) {
			continue
		}
		out = append(out, *rec(k, t[k]))
		cur = event.Cursor{Started: true, Value: atoi(k)}
		if len(out) == limit {
			break
		}
	}
	return out, cur, nil
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
