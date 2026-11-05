//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/mysql"
)

func mysqlSource(t *testing.T, dsn string) *mysql.Source {
	t.Helper()
	s, err := mysql.New(mysql.Options{DSN: dsn, ServerID: uint32(time.Now().UnixNano()%1000000) + 100000, PositionMode: "auto",
		Datasets: []config.Dataset{{Name: "users", Table: "users", PrimaryKey: "id", SensitiveColumns: []string{"api_key"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMySQLSnapshotIsConsistent(t *testing.T) {
	dsn, admin := mysqlDB(t)
	must(t, admin, "CREATE TABLE users (id bigint PRIMARY KEY, name varchar(64), api_key varchar(64), updated datetime(3)) ENGINE=InnoDB",
		"INSERT INTO users VALUES (1,'a','k1','2026-11-04 08:00:00.120'),(2,'b','k2',NULL),(3,'c','k3',NULL)")
	src := mysqlSource(t, dsn)
	ctx := context.Background()
	if err := src.Check(ctx); err != nil {
		t.Fatalf("precheck: %v", err)
	}
	sess, err := src.OpenSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	must(t, admin, "INSERT INTO users VALUES (4,'d','k4',NULL)", "UPDATE users SET name='changed' WHERE id=1")
	var all []event.Record
	cur := event.Cursor{}
	for {
		recs, next, err := sess.ReadBatch(ctx, "users", cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 0 {
			break
		}
		all = append(all, recs...)
		cur = next
	}
	if len(all) != 3 || all[0].Data["name"] != "a" || all[0].Data["updated"] != "2026-11-04 08:00:00.12" {
		t.Fatalf("snapshot saw later writes or decoded wrongly: %+v", all)
	}
}

func TestMySQLBinlogDeliversTransactions(t *testing.T) {
	dsn, admin := mysqlDB(t)
	must(t, admin, "CREATE TABLE users (id bigint PRIMARY KEY, name varchar(64), api_key varchar(64), updated datetime(3)) ENGINE=InnoDB")
	src := mysqlSource(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	from, err := src.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	must(t, admin, "BEGIN", "INSERT INTO users VALUES (1,'a','k1','2026-11-04 08:00:00.120')", "UPDATE users SET name='b' WHERE id=1", "COMMIT",
		"DELETE FROM users WHERE id=1")
	var batches []event.ChangeBatch
	err = src.Stream(ctx, from, func(b event.ChangeBatch) error {
		if len(b.Events) > 0 {
			batches = append(batches, b)
		}
		if len(batches) == 2 {
			cancel()
		}
		return nil
	})
	if len(batches) != 2 {
		t.Fatalf("batches %d err %v", len(batches), err)
	}
	if len(batches[0].Events) != 2 || batches[0].Events[1].After.Data["name"] != "b" || batches[1].Events[0].Operation != event.OpDelete {
		t.Fatalf("unexpected batches %+v", batches)
	}
	if batches[0].Events[0].After.Data["updated"] != "2026-11-04 08:00:00.12" {
		t.Fatalf("binlog datetime %v differs from snapshot form", batches[0].Events[0].After.Data["updated"])
	}
	if ok, err := src.Covers(batches[1].EndPosition, batches[0].EndPosition); !ok || err != nil {
		t.Fatalf("covers: %v %v", ok, err)
	}
}
