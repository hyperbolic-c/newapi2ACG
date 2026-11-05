package mysql

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
)

func statusResult(t *testing.T, gtid string) *gomysql.Result {
	return result(t, []string{"File", "Position", "Binlog_Do_DB", "Binlog_Ignore_DB", "Executed_Gtid_Set"},
		[][]any{{"binlog.000003", uint64(4567), "", "", gtid}})
}

func TestOpenSnapshotAlignsPosition(t *testing.T) {
	s, fc := testSource(t, "auto")
	fc.results["SHOW BINARY LOG STATUS"] = statusResult(t, "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-23,\n4e11fa47-71ca-11e1-9e33-c80aa9429562:1-5")
	sess, err := s.OpenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"SET SESSION lock_wait_timeout = 10",
		"SET SESSION time_zone = '+00:00'",
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"FLUSH TABLES WITH READ LOCK",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY",
		"SHOW BINARY LOG STATUS",
		"UNLOCK TABLES",
	}
	if !slices.Equal(fc.queries, want) {
		t.Fatalf("statements\n got %q\nwant %q", fc.queries, want)
	}
	p := sess.Position()
	if p.Mode != event.ModeGTID || p.GTIDSet != "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-23,4e11fa47-71ca-11e1-9e33-c80aa9429562:1-5" ||
		p.BinlogFile != "binlog.000003" || p.BinlogPos != 4567 {
		t.Fatalf("position %+v", p)
	}
	if err := sess.Close(); err != nil || !fc.closed {
		t.Fatal("close failed")
	}
}

func TestOpenSnapshotOlderServerAndFilePos(t *testing.T) {
	s, fc := testSource(t, "auto")
	fc.errors["SHOW BINARY LOG STATUS"] = &gomysql.MyError{Code: 1064, Message: "syntax"}
	fc.results["SHOW MASTER STATUS"] = statusResult(t, "")
	sess, err := s.OpenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := sess.Position(); p.Mode != event.ModeFilePos || p.BinlogPos != 4567 {
		t.Fatalf("position %+v", p)
	}
}

func TestOpenSnapshotFailureReleasesConnection(t *testing.T) {
	s, fc := testSource(t, "auto")
	fc.errors["FLUSH TABLES"] = &gomysql.MyError{Code: 1227, Message: "Access denied; you need RELOAD for user 'secret-user'"}
	_, err := s.OpenSnapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RELOAD") || strings.Contains(err.Error(), "secret-user") || !fc.closed {
		t.Fatalf("err=%v closed=%v", err, fc.closed)
	}
}

func TestGTIDModeRequiresGTID(t *testing.T) {
	s, fc := testSource(t, "gtid")
	fc.results["SHOW BINARY LOG STATUS"] = statusResult(t, "")
	if _, err := s.OpenSnapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "gtid_mode") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadBatchPagesByPrimaryKey(t *testing.T) {
	s, fc := testSource(t, "filepos")
	fc.results["SHOW BINARY LOG STATUS"] = statusResult(t, "")
	sess, err := s.OpenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fc.results["SELECT * FROM `tokens`"] = result(t, []string{"id", "key", "user_id", "name"},
		[][]any{{int64(7), "sk-raw-7", int64(1), "a"}, {int64(9), "sk-raw-9", int64(2), nil}})
	recs, next, err := sess.ReadBatch(context.Background(), "tokens", event.Cursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	q := fc.queries[len(fc.queries)-1]
	if q != "SELECT * FROM `tokens` ORDER BY `id` LIMIT ?" || !slices.Equal(fc.args[len(fc.args)-1], []any{2}) {
		t.Fatalf("first page query %q %v", q, fc.args[len(fc.args)-1])
	}
	if len(recs) != 2 || recs[0].Key != "7" || recs[1].Data["name"] != nil || recs[0].Data["user_id"] != int64(1) {
		t.Fatalf("records %+v", recs)
	}
	if _, ok := recs[0].Data["key"].(secrets.Value); !ok || strings.Contains(fmt.Sprint(recs), "sk-raw") {
		t.Fatal("sensitive column not protected")
	}
	if !next.Started || next.Value != int64(9) {
		t.Fatalf("cursor %+v", next)
	}
	if _, _, err := sess.ReadBatch(context.Background(), "tokens", next, 2); err != nil {
		t.Fatal(err)
	}
	q = fc.queries[len(fc.queries)-1]
	if q != "SELECT * FROM `tokens` WHERE `id` > ? ORDER BY `id` LIMIT ?" || !slices.Equal(fc.args[len(fc.args)-1], []any{int64(9), 2}) {
		t.Fatalf("next page query %q %v", q, fc.args[len(fc.args)-1])
	}
	if _, _, err := sess.ReadBatch(context.Background(), "unknown", event.Cursor{}, 2); err == nil {
		t.Fatal("unknown dataset accepted")
	}
}

func TestNewRejectsBadDSNWithoutEcho(t *testing.T) {
	for _, dsn := range []string{"u:pw-secret@h:notaport/db", "u:pw-secret@127.0.0.1:3306"} {
		_, err := New(Options{DSN: dsn, ServerID: 1})
		if err == nil || strings.Contains(err.Error(), "pw-secret") {
			t.Errorf("%q: %v", dsn, err)
		}
	}
}
