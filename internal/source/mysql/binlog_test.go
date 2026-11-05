package mysql

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
)

const uuidA = "3e11fa47-71ca-11e1-9e33-c80aa9429562"

func gset(t *testing.T, s string) gomysql.GTIDSet {
	t.Helper()
	g, err := gomysql.ParseMysqlGTIDSet(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func tokensTable(schema, table string) *replication.TableMapEvent {
	return &replication.TableMapEvent{
		Schema: []byte(schema), Table: []byte(table),
		ColumnName: [][]byte{[]byte("id"), []byte("key"), []byte("status")},
		ColumnType: []byte{gomysql.MYSQL_TYPE_LONGLONG, gomysql.MYSQL_TYPE_VARCHAR, gomysql.MYSQL_TYPE_LONGLONG},
	}
}

func ev(typ replication.EventType, logPos uint32, e replication.Event) *replication.BinlogEvent {
	return &replication.BinlogEvent{Header: &replication.EventHeader{EventType: typ, LogPos: logPos}, Event: e}
}

func feed(t *testing.T, a *assembler, events ...*replication.BinlogEvent) ([]event.ChangeBatch, error) {
	t.Helper()
	var out []event.ChangeBatch
	for _, e := range events {
		b, err := a.handle(e)
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	return out, nil
}

func TestAssemblerBuildsOneBatchPerTransaction(t *testing.T) {
	s, _ := testSource(t, "gtid")
	a := newAssembler(s, event.SourcePosition{Mode: event.ModeGTID, BinlogFile: "binlog.000001"})
	tbl := tokensTable("newapi", "tokens")
	batches, err := feed(t, a,
		ev(replication.ROTATE_EVENT, 0, &replication.RotateEvent{NextLogName: []byte("binlog.000002"), Position: 4}),
		ev(replication.QUERY_EVENT, 100, &replication.QueryEvent{Query: []byte("BEGIN")}),
		ev(replication.WRITE_ROWS_EVENTv2, 200, &replication.RowsEvent{Table: tbl, Rows: [][]any{{int64(1), "sk-new", int64(1)}}}),
		ev(replication.UPDATE_ROWS_EVENTv2, 300, &replication.RowsEvent{Table: tbl, Rows: [][]any{{int64(2), "sk-a", int64(1)}, {int64(2), "sk-a", int64(2)}}}),
		ev(replication.DELETE_ROWS_EVENTv2, 400, &replication.RowsEvent{Table: tbl, Rows: [][]any{{int64(3), "sk-b", int64(1)}}}),
		ev(replication.WRITE_ROWS_EVENTv2, 450, &replication.RowsEvent{Table: tokensTable("newapi", "logs"), Rows: [][]any{{int64(9), "x", int64(0)}}}),
		ev(replication.XID_EVENT, 500, &replication.XIDEvent{GSet: gset(t, uuidA+":1-10")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches %d", len(batches))
	}
	b := batches[0]
	want := event.SourcePosition{Mode: event.ModeGTID, GTIDSet: uuidA + ":1-10", BinlogFile: "binlog.000002", BinlogPos: 500}
	if b.EndPosition != want || len(b.Events) != 3 {
		t.Fatalf("batch %+v", b)
	}
	ops := []event.Operation{b.Events[0].Operation, b.Events[1].Operation, b.Events[2].Operation}
	if fmt.Sprint(ops) != "[insert update delete]" {
		t.Fatalf("ops %v", ops)
	}
	up := b.Events[1]
	if up.Before.Data["status"] != int64(1) || up.After.Data["status"] != int64(2) || up.After.Key != "2" || up.Position != want {
		t.Fatalf("update %+v / %+v", up.Before, up.After)
	}
	if _, ok := b.Events[0].After.Data["key"].(secrets.Value); !ok {
		t.Fatal("sensitive binlog column not protected")
	}
}

func TestAssemblerEmitsEmptyBatchesForUnrelatedTransactions(t *testing.T) {
	s, _ := testSource(t, "filepos")
	a := newAssembler(s, event.SourcePosition{Mode: event.ModeFilePos, BinlogFile: "binlog.000001"})
	batches, err := feed(t, a,
		ev(replication.QUERY_EVENT, 10, &replication.QueryEvent{Query: []byte("BEGIN")}),
		ev(replication.WRITE_ROWS_EVENTv2, 20, &replication.RowsEvent{Table: tokensTable("otherdb", "tokens"), Rows: [][]any{{int64(1), "x", int64(1)}}}),
		ev(replication.XID_EVENT, 30, &replication.XIDEvent{}),
		ev(replication.QUERY_EVENT, 40, &replication.QueryEvent{Schema: []byte("newapi"), Query: []byte("CREATE TABLE audit (id int)")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || len(batches[0].Events) != 0 || batches[0].EndPosition.BinlogPos != 30 || batches[1].EndPosition.BinlogPos != 40 {
		t.Fatalf("batches %+v", batches)
	}
}

func TestAssemblerStopsOnDDLForSyncedTable(t *testing.T) {
	s, _ := testSource(t, "filepos")
	a := newAssembler(s, event.SourcePosition{Mode: event.ModeFilePos, BinlogFile: "binlog.000001"})
	for _, q := range []string{"ALTER TABLE tokens DROP COLUMN status", "RENAME TABLE `users` TO users_old", "alter table newapi.tokens add x int"} {
		_, err := feed(t, a, ev(replication.QUERY_EVENT, 40, &replication.QueryEvent{Schema: []byte("newapi"), Query: []byte(q)}))
		if !errors.Is(err, source.ErrIncompatibleDDL) {
			t.Errorf("%q: %v", q, err)
		}
	}
	if _, err := feed(t, a, ev(replication.QUERY_EVENT, 40, &replication.QueryEvent{Schema: []byte("newapi"), Query: []byte("ALTER TABLE tokens_archive ADD x int")})); err != nil {
		t.Errorf("similar table name treated as synced: %v", err)
	}
}

func TestAssemblerRequiresRowMetadata(t *testing.T) {
	s, _ := testSource(t, "filepos")
	a := newAssembler(s, event.SourcePosition{Mode: event.ModeFilePos})
	tbl := tokensTable("newapi", "tokens")
	tbl.ColumnName = nil
	_, err := feed(t, a, ev(replication.WRITE_ROWS_EVENTv2, 1, &replication.RowsEvent{Table: tbl, Rows: [][]any{{int64(1), "x", int64(1)}}}))
	if err == nil || !strings.Contains(err.Error(), "binlog_row_metadata") {
		t.Fatalf("err=%v", err)
	}
}

func TestAssemblerHandlesCompressedPayload(t *testing.T) {
	s, _ := testSource(t, "gtid")
	a := newAssembler(s, event.SourcePosition{Mode: event.ModeGTID})
	payload := &replication.TransactionPayloadEvent{Events: []*replication.BinlogEvent{
		ev(replication.QUERY_EVENT, 0, &replication.QueryEvent{Query: []byte("BEGIN")}),
		ev(replication.WRITE_ROWS_EVENTv2, 0, &replication.RowsEvent{Table: tokensTable("newapi", "users"), Rows: [][]any{{int64(5), "x", int64(1)}}}),
		ev(replication.XID_EVENT, 0, &replication.XIDEvent{GSet: gset(t, uuidA+":1-11")}),
	}}
	batches, err := feed(t, a, ev(replication.TRANSACTION_PAYLOAD_EVENT, 900, payload))
	if err != nil || len(batches) != 1 || len(batches[0].Events) != 1 || batches[0].EndPosition.BinlogPos != 900 {
		t.Fatalf("batches %+v err %v", batches, err)
	}
}

func TestCovers(t *testing.T) {
	s, _ := testSource(t, "auto")
	g := func(set string) event.SourcePosition { return event.SourcePosition{Mode: event.ModeGTID, GTIDSet: set} }
	f := func(file string, pos uint64) event.SourcePosition {
		return event.SourcePosition{Mode: event.ModeFilePos, BinlogFile: file, BinlogPos: pos}
	}
	cases := []struct {
		applied, target event.SourcePosition
		want            bool
	}{
		{g(uuidA + ":1-10"), g(uuidA + ":1-7"), true},
		{g(uuidA + ":1-5"), g(uuidA + ":1-7"), false},
		{f("binlog.000003", 10), f("binlog.000002", 999), true},
		{f("binlog.000002", 10), f("binlog.000002", 10), true},
		{f("binlog.000002", 9), f("binlog.000002", 10), false},
	}
	for _, c := range cases {
		got, err := s.Covers(c.applied, c.target)
		if err != nil || got != c.want {
			t.Errorf("%v covers %v: %v %v", c.applied, c.target, got, err)
		}
	}
	if _, err := s.Covers(f("a.1", 1), f("b.1", 1)); err == nil {
		t.Error("different binlog base names compared")
	}
}

func TestStreamErrorMapsPurgedBinlog(t *testing.T) {
	if !errors.Is(streamError(&gomysql.MyError{Code: 1236, Message: "purged"}), source.ErrPositionLost) {
		t.Fatal("1236 not mapped to ErrPositionLost")
	}
	if errors.Is(streamError(errors.New("eof")), source.ErrPositionLost) {
		t.Fatal("generic error mapped to ErrPositionLost")
	}
}
