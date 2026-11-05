package mysql

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
)

// Stream subscribes to the binlog as a replica (go-mysql replication) and
// delivers one ChangeBatch per source transaction. go-mysql's own reconnect
// is disabled: it can resume mid-transaction in file/position mode. On any
// stream error Stream returns, and the caller restarts from its last
// committed checkpoint, which is always a transaction boundary.
func (s *Source) Stream(ctx context.Context, from event.SourcePosition, consume func(event.ChangeBatch) error) error {
	if from.IsZero() {
		return errors.New("binlog stream needs a starting position")
	}
	syncer := replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
		ServerID:                s.opts.ServerID,
		Flavor:                  gomysql.MySQLFlavor,
		Host:                    s.host,
		Port:                    s.port,
		User:                    s.user,
		Password:                s.password,
		ParseTime:               false,
		TimestampStringLocation: time.UTC,
		RenderJSONAsMySQLText:   true, // match the snapshot's JSON text
		DisableRetrySync:        true,
		HeartbeatPeriod:         30 * time.Second,
		ReadTimeout:             90 * time.Second,
		Logger:                  s.log.With("component", "go-mysql").WithGroup("binlog"),
	})
	defer syncer.Close()
	var (
		streamer *replication.BinlogStreamer
		err      error
	)
	switch from.Mode {
	case event.ModeGTID:
		set, perr := gomysql.ParseMysqlGTIDSet(from.GTIDSet)
		if perr != nil {
			return fmt.Errorf("invalid checkpoint GTID set: %w", perr)
		}
		streamer, err = syncer.StartSyncGTID(set)
	case event.ModeFilePos:
		streamer, err = syncer.StartSync(gomysql.Position{Name: from.BinlogFile, Pos: uint32(from.BinlogPos)})
	default:
		return fmt.Errorf("unknown position mode %q", from.Mode)
	}
	if err != nil {
		return streamError(err)
	}
	s.log.Info("binlog subscription started", "from", from.String())
	asm := newAssembler(s, from)
	for {
		ev, err := streamer.GetEvent(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return streamError(err)
		}
		batches, err := asm.handle(ev)
		if err != nil {
			return err
		}
		for _, b := range batches {
			if err := consume(b); err != nil {
				return err
			}
		}
	}
}

// streamError maps "binlog purged / position not found" (1236) to
// source.ErrPositionLost; everything else is a transient stream failure.
func streamError(err error) error {
	if errorCode(err) == 1236 {
		return fmt.Errorf("%w (mysql error 1236)", source.ErrPositionLost)
	}
	return fmt.Errorf("binlog stream failed: %s", describe(err))
}

// assembler turns binlog events into per-transaction batches.
type assembler struct {
	src    *Source
	mode   event.PositionMode
	file   string
	events []event.ChangeEvent
}

func newAssembler(s *Source, from event.SourcePosition) *assembler {
	return &assembler{src: s, mode: from.Mode, file: from.BinlogFile}
}

func (a *assembler) handle(ev *replication.BinlogEvent) ([]event.ChangeBatch, error) {
	if p, ok := ev.Event.(*replication.TransactionPayloadEvent); ok {
		// binlog_transaction_compression: inner events carry the transaction.
		var out []event.ChangeBatch
		for _, inner := range p.Events {
			b, err := a.handleAt(inner, ev.Header.LogPos)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil
	}
	return a.handleAt(ev, ev.Header.LogPos)
}

func (a *assembler) handleAt(ev *replication.BinlogEvent, logPos uint32) ([]event.ChangeBatch, error) {
	switch e := ev.Event.(type) {
	case *replication.RotateEvent:
		a.file = string(e.NextLogName)
	case *replication.QueryEvent:
		return a.query(e, logPos)
	case *replication.RowsEvent:
		return nil, a.rows(ev.Header.EventType, e)
	case *replication.XIDEvent:
		return a.commit(logPos, e.GSet)
	}
	return nil, nil
}

func (a *assembler) query(e *replication.QueryEvent, logPos uint32) ([]event.ChangeBatch, error) {
	q := strings.TrimSpace(string(e.Query))
	upper := strings.ToUpper(q)
	switch {
	case upper == "BEGIN" || strings.HasPrefix(upper, "XA START"):
		a.events = nil
		return nil, nil
	case upper == "COMMIT" || upper == "ROLLBACK" || strings.HasPrefix(upper, "XA COMMIT"):
		// Non-transactional tables end their "transaction" with a query event.
		return a.commit(logPos, e.GSet)
	}
	// DDL and other statements are implicit commits.
	if a.src.touches(string(e.Schema), q) {
		return nil, fmt.Errorf("%w: %s", source.ErrIncompatibleDDL, summarize(q))
	}
	return a.commit(logPos, e.GSet)
}

func (a *assembler) rows(typ replication.EventType, e *replication.RowsEvent) error {
	t := e.Table
	if t == nil || string(t.Schema) != a.src.db {
		return nil
	}
	d, ok := a.src.byTable[string(t.Table)]
	if !ok {
		return nil
	}
	names := t.ColumnNameString()
	if len(names) == 0 {
		return errors.New("binlog rows carry no column names; set binlog_row_metadata=FULL on the source")
	}
	for _, skipped := range e.SkippedColumns {
		if len(skipped) > 0 {
			return errors.New("binlog rows are partial; set binlog_row_image=FULL on the source")
		}
	}
	record := func(values []any) (*event.Record, error) { return toRecord(d, names, values, t.ColumnType) }
	sourceID := "mysql:" + a.src.db + "." + d.Table
	switch typ {
	case replication.WRITE_ROWS_EVENTv0, replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2:
		for _, row := range e.Rows {
			after, err := record(row)
			if err != nil {
				return err
			}
			a.events = append(a.events, event.ChangeEvent{SourceID: sourceID, Operation: event.OpInsert, After: after})
		}
	case replication.DELETE_ROWS_EVENTv0, replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2:
		for _, row := range e.Rows {
			before, err := record(row)
			if err != nil {
				return err
			}
			a.events = append(a.events, event.ChangeEvent{SourceID: sourceID, Operation: event.OpDelete, Before: before})
		}
	case replication.UPDATE_ROWS_EVENTv0, replication.UPDATE_ROWS_EVENTv1, replication.UPDATE_ROWS_EVENTv2:
		if len(e.Rows)%2 != 0 {
			return errors.New("malformed update rows event")
		}
		for i := 0; i < len(e.Rows); i += 2 {
			before, err := record(e.Rows[i])
			if err != nil {
				return err
			}
			after, err := record(e.Rows[i+1])
			if err != nil {
				return err
			}
			a.events = append(a.events, event.ChangeEvent{SourceID: sourceID, Operation: event.OpUpdate, Before: before, After: after})
		}
	case replication.PARTIAL_UPDATE_ROWS_EVENT:
		return errors.New("partial JSON updates are not supported; unset binlog_row_value_options on the source")
	}
	return nil
}

func (a *assembler) commit(logPos uint32, gset gomysql.GTIDSet) ([]event.ChangeBatch, error) {
	pos := event.SourcePosition{Mode: a.mode, BinlogFile: a.file, BinlogPos: uint64(logPos)}
	if a.mode == event.ModeGTID {
		if gset == nil {
			return nil, errors.New("binlog event carries no GTID set; is gtid_mode ON?")
		}
		pos.GTIDSet = gset.String()
	}
	events := a.events
	a.events = nil
	for i := range events {
		events[i].Position = pos
	}
	return []event.ChangeBatch{{Events: events, EndPosition: pos}}, nil
}

// touches reports whether a statement may change a synced table. It is
// deliberately conservative: a mention of the table name is enough.
func (s *Source) touches(schema, q string) bool {
	if schema != s.db && !containsWord(q, s.db) {
		return false
	}
	for table := range s.byTable {
		if containsWord(q, table) {
			return true
		}
	}
	return false
}

func containsWord(text, word string) bool {
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(word) + `($|[^A-Za-z0-9_$])`)
	return re.MatchString(text)
}

func summarize(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if len(q) > 160 {
		q = q[:160] + "…"
	}
	return q
}

// Covers reports whether applied includes target: GTID sets by containment,
// otherwise by binlog file sequence and offset.
func (s *Source) Covers(applied, target event.SourcePosition) (bool, error) {
	if applied.Mode == event.ModeGTID && target.Mode == event.ModeGTID {
		a, err := gomysql.ParseMysqlGTIDSet(applied.GTIDSet)
		if err != nil {
			return false, fmt.Errorf("invalid applied GTID set: %w", err)
		}
		t, err := gomysql.ParseMysqlGTIDSet(target.GTIDSet)
		if err != nil {
			return false, fmt.Errorf("invalid target GTID set: %w", err)
		}
		return a.Contain(t), nil
	}
	if applied.BinlogFile == "" || target.BinlogFile == "" {
		return false, errors.New("positions are not comparable")
	}
	cmp, err := compareFiles(applied.BinlogFile, target.BinlogFile)
	if err != nil {
		return false, err
	}
	return cmp > 0 || (cmp == 0 && applied.BinlogPos >= target.BinlogPos), nil
}

// compareFiles orders binlog files like "binlog.000012" by sequence number.
func compareFiles(a, b string) (int, error) {
	ai, bi := strings.LastIndexByte(a, '.'), strings.LastIndexByte(b, '.')
	if ai < 0 || bi < 0 || a[:ai] != b[:bi] {
		return 0, fmt.Errorf("binlog files %q and %q are not comparable", a, b)
	}
	an, err1 := strconv.ParseUint(a[ai+1:], 10, 64)
	bn, err2 := strconv.ParseUint(b[bi+1:], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("binlog files %q and %q are not comparable", a, b)
	}
	switch {
	case an < bn:
		return -1, nil
	case an > bn:
		return 1, nil
	}
	return 0, nil
}
