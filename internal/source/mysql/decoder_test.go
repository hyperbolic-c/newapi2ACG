package mysql

import (
	"math"
	"testing"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
)

func TestNormalizeMatchesSnapshotAndBinlog(t *testing.T) {
	cases := []struct {
		in   any
		typ  byte
		want any
	}{
		{nil, gomysql.MYSQL_TYPE_LONGLONG, nil},
		{int32(5), gomysql.MYSQL_TYPE_LONG, int64(5)},
		{uint64(5), gomysql.MYSQL_TYPE_LONGLONG, int64(5)},
		{uint64(math.MaxUint64), gomysql.MYSQL_TYPE_LONGLONG, uint64(math.MaxUint64)},
		{[]byte("abc"), gomysql.MYSQL_TYPE_VAR_STRING, "abc"},
		// binary protocol prints 6 fractional digits, binlog the column precision
		{[]byte("2026-11-04 08:00:00.120000"), gomysql.MYSQL_TYPE_DATETIME, "2026-11-04 08:00:00.12"},
		{"2026-11-04 08:00:00.120", gomysql.MYSQL_TYPE_DATETIME2, "2026-11-04 08:00:00.12"},
		{[]byte("2026-11-04 08:00:00.000000"), gomysql.MYSQL_TYPE_TIMESTAMP, "2026-11-04 08:00:00"},
		{"10.500", gomysql.MYSQL_TYPE_NEWDECIMAL, "10.500"}, // not temporal: unchanged
		{float32(1.5), gomysql.MYSQL_TYPE_FLOAT, float64(1.5)},
	}
	for _, c := range cases {
		if got := normalize(c.in, c.typ); got != c.want {
			t.Errorf("normalize(%#v, %d) = %#v, want %#v", c.in, c.typ, got, c.want)
		}
	}
}
