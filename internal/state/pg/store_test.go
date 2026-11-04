package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/test/contract"
)

type stateSpy struct {
	queries               []string
	args                  [][]any
	fail                  bool
	committed, rolledBack bool
}

func (s *stateSpy) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	s.queries = append(s.queries, q)
	s.args = append(s.args, args)
	if s.fail {
		return nil, errors.New("private DB data")
	}
	return affectedRows(1), nil
}
func (s *stateSpy) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unused")
}
func (s *stateSpy) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (s *stateSpy) Commit() error                                            { s.committed = true; return nil }
func (s *stateSpy) Rollback() error                                          { s.rolledBack = true; return nil }

type affectedRows int64

func (r affectedRows) RowsAffected() (int64, error) { return int64(r), nil }
func (r affectedRows) LastInsertId() (int64, error) { return 0, nil }
func spyTx(s *stateSpy) *Tx {
	return &Tx{ctx: context.Background(), sql: s, job: "job", target: "target"}
}
func TestStateValidationPoisonsTransaction(t *testing.T) {
	s := &stateSpy{}
	tx := spyTx(s)
	err := tx.ApplyMirror(1, []state.RowChange{{Dataset: "users", Key: "1", Data: map[string]any{"name": "a"}}, {Dataset: "", Key: "2"}})
	if err == nil {
		t.Fatal("invalid row accepted")
	}
	if tx.Commit() == nil || s.committed || !s.rolledBack {
		t.Fatal("partial transaction committed")
	}
}
func TestStateDatabaseErrorSanitized(t *testing.T) {
	s := &stateSpy{fail: true}
	tx := spyTx(s)
	err := tx.Advance(1, "uuid:1-2")
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("error exposed driver details")
	}
	if tx.Commit() == nil || s.committed {
		t.Fatal("failed transaction committed")
	}
}
func TestPendingUsesFreshVersionAndConditionalDelete(t *testing.T) {
	s := &stateSpy{}
	tx := spyTx(s)
	id := contract.Group("group:a", "a").Metadata.SourceIdentity
	if err := tx.Enqueue([]model.Identity{id}, "source_change"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Dequeue([]state.PendingItem{{Identity: id, Version: 12}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.queries[0], "nextval") || !strings.Contains(s.queries[1], "version=$4") {
		t.Fatal("queue version protocol missing")
	}
	if s.args[1][0] != "job" || s.args[1][1] != "target" || s.args[1][3] != int64(12) {
		t.Fatal("queue scope/version lost")
	}
}
func TestIntentRejectsDuplicateIDs(t *testing.T) {
	s := &stateSpy{}
	tx := spyTx(s)
	change := contract.Change("op", contract.Group("group:a", "a"))
	if tx.RecordIntent([]model.Change{change, change}) == nil {
		t.Fatal("duplicate OpID accepted")
	}
	if tx.Commit() == nil {
		t.Fatal("invalid intent transaction committed")
	}
}

func TestMirrorDecodePreservesLargeIntegers(t *testing.T) {
	row, err := decodeMirror("users", "1", []byte(`{"quota":9007199254740993}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(row.Data["quota"]) != "9007199254740993" {
		t.Fatal("mirror integer lost precision")
	}
}

func TestStateRejectsUnimplementedPasswordInitialization(t *testing.T) {
	r := model.Resource{User: &model.User{Initialization: &model.UserInitialization{PasswordEnv: "PASSWORD_ENV"}}}
	if _, _, err := prepareChange(model.Change{Resource: r}); err == nil || !strings.Contains(err.Error(), "initialization") {
		t.Fatal("initialization receipt silently discarded")
	}
}
func TestIntentRegistersCurrentResourceOperation(t *testing.T) {
	s := &stateSpy{}
	tx := spyTx(s)
	c := contract.Change("new-intent", contract.Group("group:a", "a"))
	if err := tx.RecordIntent([]model.Change{c}); err != nil {
		t.Fatal(err)
	}
	if len(s.queries) < 2 || !strings.Contains(s.queries[1], "migrator.resource_intents") || s.args[1][3] != "new-intent" {
		t.Fatal("new intent did not advance resource operation")
	}
}

func TestReplacingIntentTerminatesOldOperationsAndEnqueues(t *testing.T) {
	s := &stateSpy{}
	tx := spyTx(s)
	if err := tx.RecordIntent([]model.Change{contract.Change("new", contract.Group("group:a", "a"))}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.queries, "\n")
	if !strings.Contains(joined, "error_code='superseded'") || !strings.Contains(joined, "INSERT INTO migrator.pending") {
		t.Fatal("old operations left unresolved without reconciliation")
	}
}
