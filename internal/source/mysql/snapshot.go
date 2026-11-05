package mysql

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
)

// OpenSnapshot creates a consistent read aligned with a binlog position the
// way mysqldump --single-transaction --source-data does (design-core §4.4):
// a brief global read lock, start the snapshot, read the position, unlock.
// Writes on the source wait only for that short window. The account needs
// the RELOAD privilege for FLUSH TABLES WITH READ LOCK.
func (s *Source) OpenSnapshot(ctx context.Context) (source.SnapshotSession, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = c.Close() // a dropped connection also releases the read lock
		}
	}()
	steps := []struct{ sql, what string }{
		{fmt.Sprintf("SET SESSION lock_wait_timeout = %d", int(s.opts.LockWaitTimeout.Seconds())), "set lock_wait_timeout"},
		{"SET SESSION time_zone = '+00:00'", "set time_zone"},
		{"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ", "set isolation level"},
		{"FLUSH TABLES WITH READ LOCK", "take global read lock (needs RELOAD privilege)"},
		{"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY", "start consistent snapshot"},
	}
	for _, st := range steps {
		if _, err := execute(ctx, c, st.sql); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("open snapshot: %s failed: %s", st.what, describe(err))
		}
	}
	pos, err := s.readPosition(ctx, c)
	if err != nil {
		return nil, err
	}
	if _, err := execute(ctx, c, "UNLOCK TABLES"); err != nil {
		return nil, fmt.Errorf("open snapshot: release global read lock failed: %s", describe(err))
	}
	ok = true
	s.log.Info("snapshot opened", "position", pos.String())
	return &session{src: s, conn: c, pos: pos}, nil
}

type session struct {
	src    *Source
	conn   conn
	pos    event.SourcePosition
	closed bool
}

func (ss *session) Position() event.SourcePosition { return ss.pos }

func (ss *session) ReadBatch(ctx context.Context, dataset string, cur event.Cursor, limit int) ([]event.Record, event.Cursor, error) {
	if ss.closed {
		return nil, cur, errors.New("snapshot session is closed")
	}
	d, ok := ss.src.datasets[dataset]
	if !ok {
		return nil, cur, fmt.Errorf("dataset %s is not configured", dataset)
	}
	if limit <= 0 {
		return nil, cur, errors.New("batch limit must be positive")
	}
	q, args := pageQuery(d, cur, limit)
	r, err := execute(ctx, ss.conn, q, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, cur, ctx.Err()
		}
		return nil, cur, fmt.Errorf("read dataset %s failed: %s", dataset, describe(err))
	}
	if r == nil || r.Resultset == nil {
		return nil, cur, fmt.Errorf("read dataset %s: no result set", dataset)
	}
	rs := r.Resultset
	names := make([]string, len(rs.Fields))
	types := make([]byte, len(rs.Fields))
	for i, f := range rs.Fields {
		names[i], types[i] = string(f.Name), f.Type
	}
	records := make([]event.Record, 0, len(rs.Values))
	next := cur
	for _, row := range rs.Values {
		values := make([]any, len(row))
		for j := range row {
			values[j] = row[j].Value()
		}
		rec, err := toRecord(d, names, values, types)
		if err != nil {
			return nil, cur, err
		}
		records = append(records, *rec)
		next = event.Cursor{Started: true, Value: rec.Data[d.PrimaryKey]}
	}
	return records, next, nil
}

func (ss *session) Close() error {
	if ss.closed {
		return nil
	}
	ss.closed = true
	_, _ = ss.conn.Execute("COMMIT") // read-only; best effort before disconnect
	return ss.conn.Close()
}
