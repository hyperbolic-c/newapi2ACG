package mysql

import (
	"context"
	"strings"
	"testing"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

// fakeConn answers statements from a script keyed by exact text or prefix.
type fakeConn struct {
	t       *testing.T
	results map[string]*gomysql.Result
	errors  map[string]error
	queries []string
	args    [][]any
	closed  bool
}

func newFake(t *testing.T) *fakeConn {
	return &fakeConn{t: t, results: map[string]*gomysql.Result{}, errors: map[string]error{}}
}

func (f *fakeConn) Execute(q string, args ...any) (*gomysql.Result, error) {
	f.queries = append(f.queries, q)
	f.args = append(f.args, args)
	for key, err := range f.errors {
		if strings.HasPrefix(q, key) {
			return nil, err
		}
	}
	for key, r := range f.results {
		if strings.HasPrefix(q, key) {
			return r, nil
		}
	}
	return &gomysql.Result{}, nil
}

func (f *fakeConn) Close() error { f.closed = true; return nil }

func result(t *testing.T, names []string, rows [][]any) *gomysql.Result {
	t.Helper()
	rs, err := gomysql.BuildSimpleResultset(names, rows, true)
	if err != nil {
		t.Fatal(err)
	}
	// The real client fills FieldNames and parses RowDatas into Values.
	rs.FieldNames = map[string]int{}
	for i, f := range rs.Fields {
		rs.FieldNames[string(f.Name)] = i
	}
	for _, raw := range rs.RowDatas {
		values, err := raw.Parse(rs.Fields, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		rs.Values = append(rs.Values, values)
	}
	return gomysql.NewResult(rs)
}

func testSource(t *testing.T, mode string) (*Source, *fakeConn) {
	t.Helper()
	s, err := New(Options{
		DSN: "migrator:pw@127.0.0.1:3306/newapi", ServerID: 4201, PositionMode: mode,
		Datasets: []config.Dataset{
			{Name: "users", Table: "users", PrimaryKey: "id"},
			{Name: "tokens", Table: "tokens", PrimaryKey: "id", SensitiveColumns: []string{"key"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fc := newFake(t)
	s.connect = func(context.Context) (conn, error) { return fc, nil }
	return s, fc
}
