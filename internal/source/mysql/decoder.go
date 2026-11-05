package mysql

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
)

// normalize gives snapshot rows and binlog rows the same representation so
// a row compares equal whichever way it was read:
//   - integers → int64 (uint64 only above MaxInt64)
//   - FLOAT/DOUBLE → float64; DECIMAL, text, JSON, BLOB → string
//   - DATETIME/TIMESTAMP/TIME → text with trailing fractional zeros removed
//     (the binary protocol always prints six digits, binlog prints the
//     column precision); TIMESTAMP is UTC on both paths
//   - NULL → nil
//
// ENUM, SET and BIT differ between the two paths and are rejected by Check.
func normalize(v any, mysqlType byte) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return text(string(x), mysqlType)
	case string:
		return text(x, mysqlType)
	case int64:
		return x
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		if x <= math.MaxInt64 {
			return int64(x)
		}
		return x
	case float32:
		return float64(x)
	case float64:
		return x
	case time.Time:
		return text(x.UTC().Format("2006-01-02 15:04:05.999999"), mysqlType)
	case fmt.Stringer: // decimal.Decimal when UseDecimal is set
		return x.String()
	}
	return fmt.Sprint(v)
}

func text(s string, mysqlType byte) string {
	if isTemporal(mysqlType) && strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func isTemporal(t byte) bool {
	switch t {
	case gomysql.MYSQL_TYPE_DATETIME, gomysql.MYSQL_TYPE_DATETIME2,
		gomysql.MYSQL_TYPE_TIMESTAMP, gomysql.MYSQL_TYPE_TIMESTAMP2,
		gomysql.MYSQL_TYPE_TIME, gomysql.MYSQL_TYPE_TIME2:
		return true
	}
	return false
}

// toRecord builds a Record from column names, raw values and MySQL types.
// Sensitive columns become secrets.Value so they cannot leak through
// formatting or logging.
func toRecord(d config.Dataset, names []string, values []any, types []byte) (*event.Record, error) {
	if len(names) != len(values) || len(types) != len(values) {
		return nil, fmt.Errorf("dataset %s: column metadata does not match row values", d.Name)
	}
	data := make(map[string]any, len(names))
	for i, name := range names {
		v := normalize(values[i], types[i])
		if v != nil && slices.Contains(d.SensitiveColumns, name) {
			v = secrets.New(fmt.Sprint(v))
		}
		data[name] = v
	}
	pk, ok := data[d.PrimaryKey]
	if !ok {
		return nil, fmt.Errorf("dataset %s: primary key column %s not present", d.Name, d.PrimaryKey)
	}
	if pk == nil {
		return nil, fmt.Errorf("dataset %s: NULL primary key", d.Name)
	}
	return &event.Record{Dataset: d.Name, Key: fmt.Sprint(pk), Data: data}, nil
}
