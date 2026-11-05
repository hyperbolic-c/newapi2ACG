// Package source defines data collection (design-core §4, §5). Every source
// can open consistent snapshots; sources with a change log also stream.
package source

import (
	"context"
	"errors"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
)

// Source opens consistent snapshots. Full sync and reconciliation both use it.
type Source interface {
	OpenSnapshot(ctx context.Context) (SnapshotSession, error)
	// Check verifies prerequisites (connectivity, privileges, log settings)
	// and returns an error describing every problem found.
	Check(ctx context.Context) error
}

// SnapshotSession reads datasets in batches from one consistent view.
// Position is the change-log position matching that view exactly. The
// session never switches transactions between ReadBatch calls; when the
// session breaks, callers must open a new one and start over (design-core §4.4).
type SnapshotSession interface {
	Position() event.SourcePosition
	// ReadBatch returns up to limit records after cursor, ordered by primary
	// key, and the cursor to pass next. An empty result means the dataset is done.
	ReadBatch(ctx context.Context, dataset string, cursor event.Cursor, limit int) ([]event.Record, event.Cursor, error)
	Close() error
}

// Streamer is the optional change-log capability.
type Streamer interface {
	// Stream delivers one batch per source transaction, starting after from,
	// until ctx is canceled or an error occurs. Batches with no relevant
	// events are delivered too, so the position can advance.
	Stream(ctx context.Context, from event.SourcePosition, consume func(event.ChangeBatch) error) error
	// CurrentPosition is the source's latest position (catch-up target).
	CurrentPosition(ctx context.Context) (event.SourcePosition, error)
	// Covers reports whether applied includes everything up to target.
	Covers(applied, target event.SourcePosition) (bool, error)
}

var (
	// ErrPositionLost means the change log no longer contains the requested
	// position (purged binlog). Recovery is a new full sync, never skipping ahead.
	ErrPositionLost = errors.New("source change log no longer contains the checkpoint position")
	// ErrIncompatibleDDL means a schema change touched a synced table; the
	// task stops without advancing the checkpoint (design-core §12).
	ErrIncompatibleDDL = errors.New("schema change on a synced table")
)
