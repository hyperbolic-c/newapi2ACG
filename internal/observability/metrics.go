package observability

import (
	"expvar"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics are the counters needed to watch sync health (design-core §12
// "运行保障"): progress, lag indicators and retries. They are published via
// expvar under "migrator" and served at /debug/vars when the HTTP listener
// is enabled.
type Metrics struct {
	BatchesApplied    atomic.Int64
	EventsApplied     atomic.Int64
	OperationsSent    atomic.Int64
	OperationsSkipped atomic.Int64
	Retries           atomic.Int64
	ReconcileRuns     atomic.Int64
	ReconcileFixes    atomic.Int64
	OrphanedResources atomic.Int64
	LastCommitUnix    atomic.Int64
	phase             atomic.Value
	position          atomic.Value
}

var publishOnce sync.Once

func NewMetrics() *Metrics {
	m := &Metrics{}
	m.phase.Store("")
	m.position.Store("")
	return m
}

// Publish registers m with expvar (once per process).
func (m *Metrics) Publish() {
	publishOnce.Do(func() { expvar.Publish("migrator", expvar.Func(func() any { return m.Snapshot() })) })
}

func (m *Metrics) SetPhase(p string) { m.phase.Store(p) }
func (m *Metrics) SetPosition(p string) {
	m.position.Store(p)
	m.LastCommitUnix.Store(time.Now().Unix())
}

func (m *Metrics) Snapshot() map[string]any {
	return map[string]any{
		"phase":              m.phase.Load(),
		"applied_position":   m.position.Load(),
		"batches_applied":    m.BatchesApplied.Load(),
		"events_applied":     m.EventsApplied.Load(),
		"operations_sent":    m.OperationsSent.Load(),
		"operations_skipped": m.OperationsSkipped.Load(),
		"retries":            m.Retries.Load(),
		"reconcile_runs":     m.ReconcileRuns.Load(),
		"reconcile_fixes":    m.ReconcileFixes.Load(),
		"orphaned_resources": m.OrphanedResources.Load(),
		"last_commit_unix":   m.LastCommitUnix.Load(),
	}
}
