# 读取：从 MySQL 全量读出与按条件查询 New API 数据 实施计划

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现“读取”这一步的三个能力，供后面的转换和写入使用：
1. **全量读取**：在一个只读一致性读事务里读出字段清单中的全部表，逐条交给调用方，并返回读取前的 binlog 位置 G0 和所连实例的 `server_uuid`。
2. **按条件查询**：写入器回源查询某些资源当前的数据（例如“某分组的全部令牌”），每次查询在一个一致性读事务里完成，并确认连的仍是同一个 MySQL 实例。
3. **源库预检**：检查所连实例能否同时支撑全量读取、回源查询和 binlog 订阅（design 9.2）。

**Architecture:** 读取层是通用的，不包含任何 New API 专有的表名或字段名：

- 要读哪些表、哪些列、每列什么类型，全部来自项目的字段清单 `configs/sources/<项目>.yaml`；连哪个库、DATETIME 按哪个时区解释，来自实例配置 `configs/instances/*.yaml`。接入另一个项目只需新增这两类配置。
- `internal/source/contract`：与数据库无关的纯函数——核对源表字段、生成 SQL、把数据库读出的文本值转换成统一类型、判断记录是否满足查询条件。
- `internal/source/mysql`：MySQL 的连接、全量读取、按条件查询、预检。
- `internal/source/source.go`：读取层对外的接口，转换和写入只依赖这里。

本计划**不**包含：binlog 订阅（binlog 增量同步阶段）、把读取接入 `migrator run`、把 G0 写入状态库（写入子计划用已有的 `SetFullSyncStart`）。对应设计文档 3.1～3.4、9.2、9.4、9.6、11.2。

**Tech Stack:** Go 1.26；`database/sql`；`github.com/go-mysql-org/go-mysql v1.16.0`（新增）。整个项目的 MySQL 访问统一用 go-mysql：本计划用它的 `driver` 包（实现 `database/sql` 驱动）做全量读取、回源查询和预检；binlog 增量同步阶段用它的 `replication` 包订阅 binlog（design 9.3）。不引入 `go-sql-driver/mysql`。

**连接地址格式（go-mysql）：** `用户:密码@主机:端口/库名?参数`，例如 `migrator:***@10.0.0.5:3306/newapi?timeout=5s`。go-mysql 也兼容 `用户:密码@tcp(主机:端口)/库名` 写法，但带 `tcp(...)` 时不能附加参数，配置说明统一用前一种。

---

## 约定

**离线 Go 环境**（所有命令前先执行）：

```sh
export GOPATH=/private/tmp/newapi2acg-gopath
export GOMODCACHE=/private/tmp/newapi2acg-mod-cache
export GOCACHE=/private/tmp/newapi2acg-go-cache
export GOPROXY=file:///Users/liam/go/pkg/mod/cache/download
```

**不下载、不启动数据库。** 带 `integration` 标记的测试只要求编译通过；有测试库时设置 `MIGRATOR_TEST_MYSQL_DSN` 运行。

**错误信息规则：** 返回的错误只能包含代码中写定的文字、数据集名、字段名、整数主键；不得包含驱动错误原文、连接地址、列值或密钥。

**每个任务结束前**：`gofmt -l cmd internal test` 无输出；`golangci-lint run ./...` 为 `0 issues.`。

**查询条件的语义：** SQL 只负责粗筛，最终是否命中一律在 Go 中按精确规则再判断一次。这样结果不受 MySQL 排序规则影响（MySQL 默认大小写不敏感，`'VIP' = 'vip'` 为真；我们要求精确相等）。

## 文件结构

| 文件 | 职责 |
| --- | --- |
| `internal/config/types.go`、`validate.go`（修改） | 实例配置增加 `access.time_zone` |
| `schemas/config/instance.json`、`test/fixtures/configs/cases.json`（修改） | 配置结构校验同步更新 |
| `internal/source/contract/normalize.go` | `Record`、`FieldError`、`Normalizer`：文本值 → 统一类型 |
| `internal/source/contract/probe.go` | `ProbeResult`、`Probe`：字段清单与实际列比对 |
| `internal/source/contract/predicate.go` | `Predicate`、`In`、`ListContains`、`CheckPredicates`、`Matches` |
| `internal/source/contract/sql.go` | `QuoteIdent`、`PageQuery`、`LookupQuery`：显式列、保留字加引号、按主键分页 |
| `internal/source/source.go` | `Start`、`FullReader`、`Lookup`、`Querier` 接口 |
| `internal/source/mysql/open.go` | `Open`：解析 go-mysql 连接地址、关闭驱动自动重试 |
| `internal/source/mysql/probe.go` | `ProbeDatabase`：读 information_schema，要求 InnoDB |
| `internal/source/mysql/reader.go` | `Reader`：连接会话、分页读取、`Snapshot`、`Query`、实例一致性 |
| `internal/source/mysql/precheck.go` | `Precheck`：binlog 相关配置、是否从库、`server_uuid` 比较 |
| `internal/source/mysql/mysqltest/mysqltest.go` | （integration）临时 MySQL 测试库 |

---

## 任务 1：实例配置增加 DATETIME 时区

New API 的 `deleted_at` 等 DATETIME 列不带时区，按写入方所在时区解释。这个值属于实例（不同部署可能不同），放在实例配置里，默认 UTC。

**Files:**
- Modify: `internal/config/types.go`、`internal/config/validate.go`、`schemas/config/instance.json`、`test/fixtures/configs/cases.json`
- Test: `internal/config/load_test.go`

- [ ] **Step 1: 写失败测试**（追加到 `internal/config/load_test.go`）

```go
func TestInstanceTimeZone(t *testing.T) {
	load := func(access string) (*Bundle, error) {
		return LoadFiles(map[string][]byte{"instances/prod.yaml": []byte("instance: prod\nproject: new-api\naccess:\n" + access)})
	}
	b, err := load("  type: database\n  driver: mysql\n  dsn_env: SRC_DSN\n  time_zone: Asia/Shanghai\n")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := b.Instances["prod"].Access.Location()
	if err != nil || loc.String() != "Asia/Shanghai" {
		t.Fatalf("location %v %v", loc, err)
	}
	b, err = load("  type: database\n  driver: mysql\n  dsn_env: SRC_DSN\n")
	if err != nil {
		t.Fatal(err)
	}
	if loc, _ := b.Instances["prod"].Access.Location(); loc != time.UTC {
		t.Fatal("missing time_zone must default to UTC")
	}
	if _, err := load("  type: database\n  driver: mysql\n  dsn_env: SRC_DSN\n  time_zone: Mars/Base\n"); err == nil || !strings.Contains(err.Error(), "time_zone") {
		t.Fatalf("unknown zone accepted: %v", err)
	}
	if _, err := load("  type: api\n  base_url: https://x.example\n  credential_env: TOKEN\n  time_zone: UTC\n"); err == nil {
		t.Fatal("time_zone accepted for API access")
	}
}
```

如 `load_test.go` 尚未导入 `time`、`strings`，补上。

在 `test/fixtures/configs/cases.json` 数组末尾追加两条（Go 与独立 JSON Schema 校验共用）：

```json
{"name": "instance-time-zone", "schema": "instance", "valid": true,
 "config": {"instance": "prod", "project": "new-api", "access": {"type": "database", "driver": "mysql", "dsn_env": "SRC_DSN", "time_zone": "Asia/Shanghai"}}},
{"name": "instance-time-zone-not-string", "schema": "instance", "valid": false,
 "config": {"instance": "prod", "project": "new-api", "access": {"type": "database", "driver": "mysql", "dsn_env": "SRC_DSN", "time_zone": 8}}}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/config/ -run 'InstanceTimeZone|ConfigContractFixtures'`
Expected: 编译失败 `Access.Location undefined`；或 `instance-time-zone` 用例因未知字段失败。

- [ ] **Step 3: 实现**

`internal/config/types.go` 的 `Access` 增加字段与方法：

```go
type Access struct {
	Type          string `yaml:"type" json:"type"`
	Driver        string `yaml:"driver" json:"driver"`
	DSNEnv        string `yaml:"dsn_env" json:"dsn_env"`
	TimeZone      string `yaml:"time_zone" json:"time_zone"`
	BaseURL       string `yaml:"base_url" json:"base_url"`
	CredentialEnv string `yaml:"credential_env" json:"credential_env"`
}

// Location is the zone the source writes DATETIME values in; UTC when unset.
func (a Access) Location() (*time.Location, error) {
	if a.TimeZone == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(a.TimeZone)
}
```

（`types.go` 增加 `import "time"`。）

`internal/config/validate.go`：文件顶部导入 `_ "time/tzdata"`（把时区数据编进程序，容器里没有系统时区库也能用）；`Instance.Validate` 的 `case "database":` 分支在 `return envName(...)` 之前加入：

```go
		if _, err := i.Access.Location(); err != nil {
			return fmt.Errorf("access.time_zone: unknown IANA time zone")
		}
```

`case "api":` 分支的第一个判断改为：

```go
		if i.Access.Driver != "" || i.Access.DSNEnv != "" || i.Access.TimeZone != "" {
			return fmt.Errorf("access: database settings are invalid for API access")
		}
```

`schemas/config/instance.json`：`access.properties` 中加入

```json
"time_zone": {"type": "string", "minLength": 1}
```

并在 `allOf[0].else.not.anyOf` 数组中追加 `{"required": ["time_zone"]}`。

- [ ] **Step 4: 运行，确认通过**

Run:
```sh
go test -race ./internal/config/...
uv run --no-project --with-requirements test/schema/requirements.txt python test/schema/check.py
```
Expected: Go 测试全部 PASS；Schema 输出 `... 21 shared fixtures ... PASS (structural)`。

- [ ] **Step 5: 提交**

```bash
git add internal/config schemas/config/instance.json test/fixtures/configs/cases.json
git commit -m "feat(config): per-instance DATETIME time zone"
```

---

## 任务 2：把源数据转换成统一类型

转换规则（design 3.2）：`int` → `int64`；`bool` 只接受 `0`/`1`；`string` 必须是合法 UTF-8；`json` 用 `UseNumber` 解码、拒绝尾随数据，可空字段的空串视为 NULL；`timestamp` 按实例时区解析 DATETIME 文本，输出 UTC 的 RFC 3339；`secret` 只进 `Secrets`，永远不进 `Fields`。源库缺的可选列取默认值或 nil，缺的必需列报错。

**Files:**
- Create: `internal/source/contract/normalize.go`
- Test: `internal/source/contract/normalize_test.go`

- [ ] **Step 1: 写失败测试**

```go
package contract

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

// A made-up dataset: the reader must not depend on any project's names.
func itemsDecl() config.Dataset {
	return config.Dataset{
		PrimaryKey: "id",
		Schema: map[string]config.Field{
			"id":         {Type: "int", Required: true},
			"enabled":    {Type: "bool", Required: true},
			"name":       {Type: "string", Required: true},
			"token":      {Type: "secret", Required: true},
			"options":    {Type: "json", Required: true, Nullable: true},
			"removed_at": {Type: "timestamp", Nullable: true},
			"group":      {Type: "string", Default: "default"},
			"weight":     {Type: "int", Default: 7},
		},
	}
}

// baseRow returns a valid row; overrides replace single raw values (nil = SQL NULL).
func baseRow(overrides map[string][]byte) ([]string, [][]byte) {
	cols := []string{"id", "enabled", "name", "token", "options"}
	raw := [][]byte{[]byte("42"), []byte("1"), []byte("name"), []byte("sk-raw"), []byte(`{}`)}
	for i, c := range cols {
		if v, ok := overrides[c]; ok {
			raw[i] = v
		}
	}
	return cols, raw
}

func TestNormalizeTypes(t *testing.T) {
	n := Normalizer{Dataset: "items", Decl: itemsDecl(), Location: time.FixedZone("CST", 8*3600)}
	rec, err := n.Normalize(
		[]string{"id", "enabled", "name", "token", "options", "removed_at"},
		[][]byte{[]byte("42"), []byte("1"), []byte("名称"), []byte("sk-raw"), []byte(`{"a":"b","n":1}`), []byte("2026-11-04 08:00:00.123")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Dataset != "items" || rec.Key != "42" || rec.Fields["id"] != int64(42) || rec.Fields["enabled"] != true || rec.Fields["name"] != "名称" {
		t.Fatalf("unexpected fields %#v", rec.Fields)
	}
	opts, ok := rec.Fields["options"].(map[string]any)
	if !ok || opts["a"] != "b" || opts["n"] != json.Number("1") {
		t.Fatalf("json not decoded with numbers preserved: %#v", rec.Fields["options"])
	}
	if rec.Fields["removed_at"] != "2026-11-04T00:00:00.123Z" {
		t.Fatalf("timestamp not converted to UTC: %v", rec.Fields["removed_at"])
	}
	if _, ok := rec.Fields["token"]; ok {
		t.Fatal("secret leaked into fields")
	}
	if rec.Secrets["token"].Reveal() != "sk-raw" {
		t.Fatal("secret not captured")
	}
	if rec.Fields["group"] != "default" || rec.Fields["weight"] != int64(7) {
		t.Fatalf("defaults not applied for absent optional columns: %#v", rec.Fields)
	}
	if strings.Contains(fmt.Sprintf("%v %+v", rec, rec), "sk-raw") {
		t.Fatal("formatting a record revealed the secret")
	}
}

func TestNormalizeNull(t *testing.T) {
	n := Normalizer{Dataset: "items", Decl: itemsDecl()}
	cols, raw := baseRow(map[string][]byte{"options": nil})
	rec, err := n.Normalize(cols, raw)
	if err != nil {
		t.Fatal(err)
	}
	if v, present := rec.Fields["options"]; !present || v != nil {
		t.Fatalf("nullable NULL must be present as nil: %#v", rec.Fields)
	}
	cols, raw = baseRow(map[string][]byte{"enabled": nil})
	if _, err := n.Normalize(cols, raw); err == nil || !strings.Contains(err.Error(), "items.enabled") {
		t.Fatalf("NULL in non-nullable field accepted or unlabeled: %v", err)
	}
}

func TestNormalizeRejectsBadValuesWithoutEcho(t *testing.T) {
	n := Normalizer{Dataset: "items", Decl: itemsDecl()}
	for column, value := range map[string]string{
		"id":      "secret-xyz-1",
		"enabled": "2",
		"name":    "\xff\xfe",
		"options": `{"a":1} secret-trailing`,
		"token":   "\xffsecret",
	} {
		cols, raw := baseRow(map[string][]byte{column: []byte(value)})
		_, err := n.Normalize(cols, raw)
		if err == nil {
			t.Errorf("%s: bad value accepted", column)
			continue
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), value) {
			t.Errorf("%s: error echoes the raw value: %v", column, err)
		}
		if !strings.Contains(err.Error(), "items."+column) {
			t.Errorf("%s: error does not name the field: %v", column, err)
		}
	}
	cols, raw := baseRow(nil)
	if _, err := n.Normalize(append(cols, "removed_at"), append(raw, []byte("0000-00-00 00:00:00"))); err == nil {
		t.Fatal("zero DATETIME accepted")
	}
}

func TestNormalizeEmptyJSON(t *testing.T) {
	n := Normalizer{Dataset: "items", Decl: itemsDecl()}
	cols, raw := baseRow(map[string][]byte{"options": []byte("  ")})
	rec, err := n.Normalize(cols, raw)
	if err != nil || rec.Fields["options"] != nil {
		t.Fatalf("empty JSON in nullable field must become nil: %v", err)
	}
	decl := itemsDecl()
	decl.Schema["options"] = config.Field{Type: "json", Required: true}
	if _, err := (Normalizer{Dataset: "items", Decl: decl}).Normalize(cols, raw); err == nil {
		t.Fatal("empty JSON in non-nullable field accepted")
	}
}

func TestNormalizeColumnSet(t *testing.T) {
	n := Normalizer{Dataset: "items", Decl: itemsDecl()}
	if _, err := n.Normalize([]string{"id", "enabled", "token", "options"},
		[][]byte{[]byte("1"), []byte("1"), []byte("k"), []byte("{}")}); err == nil || !strings.Contains(err.Error(), "items.name") {
		t.Fatalf("missing required column not reported: %v", err)
	}
	cols, raw := baseRow(nil)
	if _, err := n.Normalize(append(cols, "balance"), append(raw, []byte("1"))); err == nil {
		t.Fatal("undeclared column accepted")
	}
	if _, err := n.Normalize(append(cols, "name"), append(raw, []byte("dup"))); err == nil {
		t.Fatal("duplicate column accepted")
	}
	if _, err := n.Normalize(cols, raw[:2]); err == nil {
		t.Fatal("mismatched column/value counts accepted")
	}
}

func TestNormalizeNullSecret(t *testing.T) {
	decl := itemsDecl()
	decl.Schema["token"] = config.Field{Type: "secret", Required: true, Nullable: true}
	cols, raw := baseRow(map[string][]byte{"token": nil})
	rec, err := (Normalizer{Dataset: "items", Decl: decl}).Normalize(cols, raw)
	if err != nil {
		t.Fatal(err)
	}
	if v, present := rec.Fields["token"]; !present || v != nil || len(rec.Secrets) != 0 {
		t.Fatalf("NULL secret must appear as a nil field without a secret value: %#v", rec.Fields)
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/source/contract/ -run Normalize`
Expected: 编译失败，`undefined: Normalizer`。

- [ ] **Step 3: 实现** `internal/source/contract/normalize.go`

```go
// Package contract turns a project's field list (configs/sources/*.yaml) into
// SQL and normalized records. It knows nothing about any specific project.
package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
)

// Record is one source row in normalized types:
// int → int64, bool → bool, string → string, json → decoded value with
// numbers as json.Number, timestamp → RFC 3339 UTC string, SQL NULL → nil.
// Non-null secrets live only in Secrets. A NULL secret appears in Fields as
// nil so callers can tell "no secret" from "secret present".
type Record struct {
	Dataset string
	Key     string
	Fields  map[string]any
	Secrets map[string]secrets.Value
}

// FieldError names the dataset, field and problem, never the raw value.
type FieldError struct{ Dataset, Field, Problem string }

func (e *FieldError) Error() string {
	return fmt.Sprintf("datasets.%s.%s: %s", e.Dataset, e.Field, e.Problem)
}

// Normalizer converts text-protocol column values. Location is the zone the
// source wrote DATETIME values in; nil means UTC.
type Normalizer struct {
	Dataset  string
	Decl     config.Dataset
	Location *time.Location
}

const datetimeLayout = "2006-01-02 15:04:05.999999999"

// Normalize converts one row. columns are the selected source columns;
// raw[i] == nil means SQL NULL. Declared fields the source lacks get their
// default (or nil); a missing required field is an error.
func (n Normalizer) Normalize(columns []string, raw [][]byte) (Record, error) {
	if len(columns) != len(raw) {
		return Record{}, fmt.Errorf("datasets.%s: column and value counts differ", n.Dataset)
	}
	rec := Record{Dataset: n.Dataset, Fields: map[string]any{}, Secrets: map[string]secrets.Value{}}
	seen := make(map[string]bool, len(columns))
	for i, name := range columns {
		decl, ok := n.Decl.Schema[name]
		if !ok {
			return Record{}, &FieldError{n.Dataset, name, "column is not declared in the field list"}
		}
		if seen[name] {
			return Record{}, &FieldError{n.Dataset, name, "column selected twice"}
		}
		seen[name] = true
		if err := n.put(&rec, name, decl, raw[i]); err != nil {
			return Record{}, err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(n.Decl.Schema)) {
		if seen[name] {
			continue
		}
		decl := n.Decl.Schema[name]
		if decl.Required {
			return Record{}, &FieldError{n.Dataset, name, "required column missing from source"}
		}
		v, err := defaultValue(decl)
		if err != nil {
			return Record{}, &FieldError{n.Dataset, name, err.Error()}
		}
		rec.Fields[name] = v
	}
	key, ok := rec.Fields[n.Decl.PrimaryKey].(int64)
	if !ok {
		return Record{}, &FieldError{n.Dataset, n.Decl.PrimaryKey, "primary key must be a non-null int"}
	}
	rec.Key = strconv.FormatInt(key, 10)
	return rec, nil
}

func (n Normalizer) put(rec *Record, name string, decl config.Field, raw []byte) error {
	if raw == nil {
		if !decl.Nullable {
			return &FieldError{n.Dataset, name, "NULL in a non-nullable field"}
		}
		rec.Fields[name] = nil
		return nil
	}
	if decl.Type == "secret" {
		if !utf8.Valid(raw) {
			return &FieldError{n.Dataset, name, "secret is not valid UTF-8"}
		}
		rec.Secrets[name] = secrets.New(string(raw))
		return nil
	}
	v, err := n.convert(decl, raw)
	if err != nil {
		return &FieldError{n.Dataset, name, err.Error()}
	}
	rec.Fields[name] = v
	return nil
}

func (n Normalizer) convert(decl config.Field, raw []byte) (any, error) {
	switch decl.Type {
	case "int":
		v, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return nil, errors.New("not a 64-bit integer")
		}
		return v, nil
	case "bool":
		switch string(raw) {
		case "0":
			return false, nil
		case "1":
			return true, nil
		}
		return nil, errors.New("boolean must be 0 or 1")
	case "string":
		if !utf8.Valid(raw) {
			return nil, errors.New("not valid UTF-8")
		}
		return string(raw), nil
	case "json":
		if len(bytes.TrimSpace(raw)) == 0 {
			if decl.Nullable {
				return nil, nil
			}
			return nil, errors.New("empty JSON in a non-nullable field")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, errors.New("invalid JSON")
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, errors.New("trailing data after JSON value")
		}
		return v, nil
	case "timestamp":
		loc := n.Location
		if loc == nil {
			loc = time.UTC
		}
		t, err := time.ParseInLocation(datetimeLayout, string(raw), loc)
		if err != nil {
			return nil, errors.New("not a DATETIME value")
		}
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	return nil, errors.New("unsupported field type")
}

// defaultValue converts a YAML default to the normalized representation.
// Secret defaults are rejected by config validation.
func defaultValue(decl config.Field) (any, error) {
	switch v := decl.Default.(type) {
	case nil:
		return nil, nil
	case int:
		return int64(v), nil
	case string:
		if decl.Type == "timestamp" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, errors.New("invalid timestamp default")
			}
			return t.UTC().Format(time.RFC3339Nano), nil
		}
		return v, nil
	}
	return decl.Default, nil
}
```

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/source/contract/ -run Normalize -v`
Expected: 6 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/source/contract/normalize.go internal/source/contract/normalize_test.go
git commit -m "feat(source): convert source rows to field-list types"
```

---

## 任务 3：核对源表字段、查询条件与 SQL 生成

- **核对字段**：输入字段清单和“表 → 数据库里实际有的列”，输出四类：要查询的列；源库没有、可用默认值的列；源库没有、又必须要的列（阻止迁移）；源库多出来、清单里既没要也没明确排除的列（告警，可能是上游新增字段，design 3.4）。MySQL 列名大小写不敏感，按小写比对。
- **查询条件**：两种通用条件，转换层按项目需要组合（多个条件为“且”）：
  - `In(field, values...)`：字段值等于其中之一。
  - `ListContains(field, value, sep)`：字段是用 `sep` 分隔的列表（如 `"default,vip"`），去掉每项首尾空格后包含 `value`。
  - 只能用于源库存在的、非密钥的 `int` / `string` / `bool` 字段；`ListContains` 只能用于 `string`。
- **SQL**：显式列名、保留字加引号、按主键分页。`In` 生成 `IN (?,…)`，值列表为空时直接判定无结果、不发查询；`ListContains` 生成 `LIKE ? ESCAPE '!'` 粗筛，Go 中再精确判断。

**Files:**
- Create: `internal/source/contract/probe.go`、`internal/source/contract/predicate.go`、`internal/source/contract/sql.go`
- Test: `internal/source/contract/probe_test.go`、`internal/source/contract/predicate_test.go`、`internal/source/contract/sql_test.go`

- [ ] **Step 1: 写失败测试**

`internal/source/contract/probe_test.go`：

```go
package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

func probeSource() config.Source {
	d := itemsDecl()
	d.Dropped = []string{"balance"}
	d.Bindings.Database.Table = "items"
	return config.Source{Datasets: map[string]config.Dataset{"items": d}}
}

func TestProbeClassifiesColumns(t *testing.T) {
	r := Probe(probeSource(), map[string][]string{
		"items": {"ID", "enabled", "name", "token", "options", "group", "balance", "new_column"},
	})
	if err := r.Blocking(); err != nil {
		t.Fatalf("unexpected blocking result: %v", err)
	}
	if !slices.Equal(r.Present["items"], []string{"enabled", "group", "id", "name", "options", "token"}) {
		t.Fatalf("present: %v", r.Present["items"])
	}
	if !slices.Equal(r.Absent["items"], []string{"removed_at", "weight"}) {
		t.Fatalf("absent optional: %v", r.Absent["items"])
	}
	if !slices.Equal(r.Unknown["items"], []string{"new_column"}) {
		t.Fatalf("unknown (dropped must not be reported): %v", r.Unknown["items"])
	}
}

func TestProbeBlocksMissingRequired(t *testing.T) {
	r := Probe(probeSource(), map[string][]string{"items": {"id", "enabled", "token", "options"}})
	if err := r.Blocking(); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("missing required column not blocking: %v", err)
	}
}

func TestProbeBlocksMissingTable(t *testing.T) {
	if err := Probe(probeSource(), map[string][]string{"other": {"id"}}).Blocking(); err == nil || !strings.Contains(err.Error(), "items") {
		t.Fatalf("missing table not blocking: %v", err)
	}
}

func TestProbeRequiresIntPrimaryKey(t *testing.T) {
	src := probeSource()
	d := src.Datasets["items"]
	d.Schema["id"] = config.Field{Type: "string", Required: true}
	src.Datasets["items"] = d
	r := Probe(src, map[string][]string{"items": {"id", "enabled", "name", "token", "options"}})
	if err := r.Blocking(); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Fatalf("non-int primary key accepted: %v", err)
	}
}
```

`internal/source/contract/predicate_test.go`：

```go
package contract

import (
	"strings"
	"testing"
)

var present = []string{"enabled", "group", "id", "name", "options", "token"}

func TestCheckPredicatesAcceptsSupportedFilters(t *testing.T) {
	err := CheckPredicates("items", itemsDecl(), present, []Predicate{
		In("id", int64(1), int64(2)), In("name", "a"), In("enabled", true), ListContains("group", "vip", ","),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckPredicatesRejects(t *testing.T) {
	for name, p := range map[string]Predicate{
		"secret field":       In("token", "x"),
		"undeclared field":   In("nope", "x"),
		"absent in source":   In("weight", int64(1)),
		"type mismatch":      In("id", "1"),
		"json not filtered":  In("options", "x"),
		"list on int":        ListContains("id", "1", ","),
		"list no separator":  ListContains("group", "vip", ""),
		"unknown op":         {Field: "id", Op: 99, Values: []any{int64(1)}},
	} {
		err := CheckPredicates("items", itemsDecl(), present, []Predicate{p})
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "items") {
			t.Errorf("%s: error does not name the dataset: %v", name, err)
		}
	}
}

func TestMatchesIsExact(t *testing.T) {
	rec := Record{Fields: map[string]any{"id": int64(3), "name": "vip", "group": " default , VIP2,vip "}}
	cases := []struct {
		p    Predicate
		want bool
	}{
		{In("id", int64(1), int64(3)), true},
		{In("id", int64(4)), false},
		{In("name", "VIP"), false}, // exact, even though MySQL collation would match
		{ListContains("group", "vip", ","), true},
		{ListContains("group", "default", ","), true},
		{ListContains("group", "VIP", ","), false},
		{ListContains("group", "vi", ","), false}, // whole items only
	}
	for _, tc := range cases {
		if got := tc.p.Matches(rec); got != tc.want {
			t.Errorf("%+v: got %v", tc.p, got)
		}
	}
}
```

`internal/source/contract/sql_test.go`：

```go
package contract

import (
	"slices"
	"testing"
)

func TestPageQueryQuotesReservedWords(t *testing.T) {
	q, err := PageQuery(MySQL, "items", "id", []string{"id", "group", "key"})
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT `id`,`group`,`key` FROM `items` WHERE `id` > ? ORDER BY `id` LIMIT ?"
	if q != want {
		t.Fatalf("got  %s\nwant %s", q, want)
	}
}

func TestLookupQuery(t *testing.T) {
	q, args, empty, err := LookupQuery(MySQL, "items", "id", []string{"id", "group"},
		[]Predicate{In("id", int64(1), int64(2)), ListContains("group", "50%_off!", ",")})
	if err != nil || empty {
		t.Fatal(err, empty)
	}
	want := "SELECT `id`,`group` FROM `items` WHERE `id` IN (?,?) AND `group` LIKE ? ESCAPE '!' AND `id` > ? ORDER BY `id` LIMIT ?"
	if q != want {
		t.Fatalf("got  %s\nwant %s", q, want)
	}
	if !slices.Equal(args, []any{int64(1), int64(2), "%50!%!_off!!%"}) {
		t.Fatalf("args %v", args)
	}
}

func TestLookupQueryEmptyIn(t *testing.T) {
	_, _, empty, err := LookupQuery(MySQL, "items", "id", []string{"id"}, []Predicate{In("id")})
	if err != nil || !empty {
		t.Fatalf("empty IN must short-circuit: empty=%v err=%v", empty, err)
	}
}

func TestQueriesRejectUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		table, pk string
		cols      []string
	}{
		{"items; DROP TABLE x", "id", []string{"id"}},
		{"items", "id", []string{"id", "na`me"}},
		{"items", "id", []string{"name"}}, // primary key not selected
		{"items", "id", nil},
	} {
		if _, err := PageQuery(MySQL, tc.table, tc.pk, tc.cols); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	if _, _, _, err := LookupQuery(MySQL, "items", "id", []string{"id"}, []Predicate{In("bad`field", int64(1))}); err == nil {
		t.Error("unsafe filter field accepted")
	}
	if _, err := PageQuery(Dialect(99), "items", "id", []string{"id"}); err == nil {
		t.Error("unknown dialect accepted")
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/source/contract/`
Expected: 编译失败，`undefined: Probe`、`undefined: In`、`undefined: PageQuery`。

- [ ] **Step 3: 实现**

`internal/source/contract/probe.go`：

```go
package contract

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

// ProbeResult compares a project's field list with the source's columns.
// Present: declared fields to SELECT. Absent: optional declared fields the
// source lacks (defaults apply). Missing: required fields the source lacks
// (blocking). Unknown: source columns neither declared nor dropped (warning:
// often a new upstream field, design 3.4).
type ProbeResult struct {
	Present  map[string][]string
	Absent   map[string][]string
	Missing  map[string][]string
	Unknown  map[string][]string
	Problems []string
}

// Blocking returns an error when reading must not proceed.
func (r ProbeResult) Blocking() error {
	parts := slices.Clone(r.Problems)
	for _, ds := range slices.Sorted(maps.Keys(r.Missing)) {
		parts = append(parts, fmt.Sprintf("%s: missing required columns %v", ds, r.Missing[ds]))
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("source field check failed: %s", strings.Join(parts, "; "))
}

// Probe maps table names to the columns the database reports. Column names
// match case-insensitively (MySQL semantics); table names match exactly.
func Probe(src config.Source, actual map[string][]string) ProbeResult {
	r := ProbeResult{Present: map[string][]string{}, Absent: map[string][]string{}, Missing: map[string][]string{}, Unknown: map[string][]string{}}
	for _, name := range slices.Sorted(maps.Keys(src.Datasets)) {
		d := src.Datasets[name]
		if pk, ok := d.Schema[d.PrimaryKey]; !ok || pk.Type != "int" {
			r.Problems = append(r.Problems, name+": primary key must be an int field")
		}
		table := d.Bindings.Database.Table
		cols, ok := actual[table]
		if !ok {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: table %s not found", name, table))
			continue
		}
		have := make(map[string]bool, len(cols))
		for _, c := range cols {
			have[strings.ToLower(c)] = true
		}
		known := map[string]bool{}
		for _, f := range slices.Sorted(maps.Keys(d.Schema)) {
			known[strings.ToLower(f)] = true
			switch {
			case have[strings.ToLower(f)]:
				r.Present[name] = append(r.Present[name], f)
			case d.Schema[f].Required:
				r.Missing[name] = append(r.Missing[name], f)
			default:
				r.Absent[name] = append(r.Absent[name], f)
			}
		}
		for _, f := range d.Dropped {
			known[strings.ToLower(f)] = true
		}
		for _, c := range slices.Sorted(slices.Values(cols)) {
			if !known[strings.ToLower(c)] {
				r.Unknown[name] = append(r.Unknown[name], c)
			}
		}
	}
	return r
}
```

`internal/source/contract/predicate.go`：

```go
package contract

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

type PredicateOp int

const (
	OpIn           PredicateOp = iota + 1 // field equals one of Values
	OpListContains                        // field is a Separator-joined list containing Values[0]
)

// Predicate is one lookup condition; several predicates are ANDed.
type Predicate struct {
	Field     string
	Op        PredicateOp
	Values    []any
	Separator string
}

func In(field string, values ...any) Predicate {
	return Predicate{Field: field, Op: OpIn, Values: values}
}

func ListContains(field, value, separator string) Predicate {
	return Predicate{Field: field, Op: OpListContains, Values: []any{value}, Separator: separator}
}

// CheckPredicates rejects filters the reader cannot evaluate exactly.
// present lists the dataset's fields that exist in the source.
func CheckPredicates(dataset string, d config.Dataset, present []string, preds []Predicate) error {
	for _, p := range preds {
		decl, ok := d.Schema[p.Field]
		if !ok || !slices.Contains(present, p.Field) {
			return fmt.Errorf("datasets.%s.%s: cannot filter on a field the source does not provide", dataset, p.Field)
		}
		if decl.Type == "secret" {
			return fmt.Errorf("datasets.%s.%s: cannot filter on a secret field", dataset, p.Field)
		}
		switch p.Op {
		case OpIn:
			for _, v := range p.Values {
				if !valueMatchesType(decl.Type, v) {
					return fmt.Errorf("datasets.%s.%s: filter value does not match field type %s", dataset, p.Field, decl.Type)
				}
			}
		case OpListContains:
			_, isString := firstString(p.Values)
			if decl.Type != "string" || len(p.Values) != 1 || !isString || p.Separator == "" {
				return fmt.Errorf("datasets.%s.%s: list filter needs a string field, one string value and a separator", dataset, p.Field)
			}
		default:
			return fmt.Errorf("datasets.%s.%s: unsupported filter", dataset, p.Field)
		}
	}
	return nil
}

func valueMatchesType(fieldType string, v any) bool {
	switch fieldType {
	case "int":
		_, ok := v.(int64)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	}
	return false // json and timestamp fields are not filterable
}

func firstString(values []any) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	s, ok := values[0].(string)
	return s, ok
}

// Matches applies the exact semantics; SQL only prefilters.
func (p Predicate) Matches(rec Record) bool {
	v := rec.Fields[p.Field]
	switch p.Op {
	case OpIn:
		return slices.Contains(p.Values, v)
	case OpListContains:
		s, ok := v.(string)
		want, wantOK := firstString(p.Values)
		if !ok || !wantOK {
			return false
		}
		for _, item := range strings.Split(s, p.Separator) {
			if strings.TrimSpace(item) == want {
				return true
			}
		}
	}
	return false
}

// MatchesAll reports whether rec satisfies every predicate.
func MatchesAll(preds []Predicate, rec Record) bool {
	for _, p := range preds {
		if !p.Matches(rec) {
			return false
		}
	}
	return true
}
```

`internal/source/contract/sql.go`：

```go
package contract

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Dialect selects identifier quoting. Placeholders are "?" for MySQL.
type Dialect int

const (
	MySQL Dialect = iota + 1
)

var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// QuoteIdent validates and quotes an identifier. Identifiers come from the
// field list, but are still validated: quoting alone is not the defense.
func QuoteIdent(d Dialect, name string) (string, error) {
	if !identPattern.MatchString(name) {
		return "", fmt.Errorf("invalid identifier %q", name)
	}
	switch d {
	case MySQL:
		return "`" + name + "`", nil
	}
	return "", errors.New("unsupported SQL dialect")
}

var likeEscaper = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")

// LookupQuery builds a keyset-paginated, filtered query. Callers append
// (cursor, limit) to args. empty is true when an IN list has no values: the
// result is known to be empty and no query should run.
func LookupQuery(d Dialect, table, pk string, columns []string, preds []Predicate) (query string, args []any, empty bool, err error) {
	if !slices.Contains(columns, pk) {
		return "", nil, false, errors.New("primary key must be among the selected columns")
	}
	qt, err := QuoteIdent(d, table)
	if err != nil {
		return "", nil, false, err
	}
	qpk, err := QuoteIdent(d, pk)
	if err != nil {
		return "", nil, false, err
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		if quoted[i], err = QuoteIdent(d, c); err != nil {
			return "", nil, false, err
		}
	}
	var conds []string
	for _, p := range preds {
		qf, err := QuoteIdent(d, p.Field)
		if err != nil {
			return "", nil, false, err
		}
		switch p.Op {
		case OpIn:
			if len(p.Values) == 0 {
				return "", nil, true, nil
			}
			conds = append(conds, qf+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(p.Values)), ",")+")")
			args = append(args, p.Values...)
		case OpListContains:
			v, ok := firstString(p.Values)
			if !ok {
				return "", nil, false, errors.New("list filter needs a string value")
			}
			conds = append(conds, qf+" LIKE ? ESCAPE '!'")
			args = append(args, "%"+likeEscaper.Replace(v)+"%")
		default:
			return "", nil, false, errors.New("unsupported filter")
		}
	}
	conds = append(conds, qpk+" > ?")
	query = fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT ?", strings.Join(quoted, ","), qt, strings.Join(conds, " AND "), qpk)
	return query, args, false, nil
}

// PageQuery is the unfiltered full-read query taking (cursor, limit).
// It never uses SELECT * or OFFSET (design 3.2).
func PageQuery(d Dialect, table, pk string, columns []string) (string, error) {
	q, _, _, err := LookupQuery(d, table, pk, columns, nil)
	return q, err
}
```

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/source/contract/ -v`
Expected: 任务 2、3 的全部测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/source/contract/
git commit -m "feat(source): field-list probe, lookup predicates and SQL builders"
```

---

## 任务 4：用 New API 的字段清单验证通用性

读取层代码里没有任何 New API 的名字；这里只靠 `configs/sources/new-api.yaml` 证明它能为 New API 生成全部查询。以后接入其他项目，照此为其字段清单加一个同样的测试。

**Files:**
- Test: `internal/source/contract/newapi_config_test.go`

- [ ] **Step 1: 写测试**

```go
package contract

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

func TestNewAPIFieldListBuildsQueries(t *testing.T) {
	bundle, err := config.LoadDir("../../../configs")
	if err != nil {
		t.Fatal(err)
	}
	src, ok := bundle.Sources["new-api"]
	if !ok {
		t.Fatal("configs/sources/new-api.yaml not loaded")
	}
	for _, name := range slices.Sorted(maps.Keys(src.Datasets)) {
		d := src.Datasets[name]
		cols := slices.Sorted(maps.Keys(d.Schema))
		q, err := PageQuery(MySQL, d.Bindings.Database.Table, d.PrimaryKey, cols)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, reserved := range []string{"group", "key"} {
			if slices.Contains(cols, reserved) && !strings.Contains(q, "`"+reserved+"`") {
				t.Errorf("%s: reserved column %s not quoted: %s", name, reserved, q)
			}
		}
	}
	// The lookups the New API mapping is expected to need (design 9.6).
	lookups := map[string][]Predicate{
		"channels": {ListContains("models", "gpt-4o", ","), ListContains("group", "default", ",")},
		"tokens":   {In("user_id", int64(1)), In("group", "default", "vip")},
		"users":    {In("id", int64(1), int64(2)), In("group", "default")},
	}
	for name, preds := range lookups {
		d := src.Datasets[name]
		present := slices.Sorted(maps.Keys(d.Schema))
		for _, p := range preds {
			if err := CheckPredicates(name, d, present, []Predicate{p}); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if _, _, _, err := LookupQuery(MySQL, d.Bindings.Database.Table, d.PrimaryKey, present, []Predicate{p}); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}
```

- [ ] **Step 2: 运行**

Run: `go test ./internal/source/contract/ -run NewAPI -v`
Expected: PASS。若失败，说明字段清单缺少查询所需字段或类型不对，修正 `configs/sources/new-api.yaml`（不是改代码）。

- [ ] **Step 3: 提交**

```bash
git add internal/source/contract/newapi_config_test.go
git commit -m "test(source): New API field list builds every needed query"
```

---

## 任务 5：连接 MySQL 并读取表结构

**Files:**
- Modify: `go.mod`、`go.sum`
- Create: `internal/source/source.go`、`internal/source/mysql/open.go`、`internal/source/mysql/probe.go`、`internal/source/mysql/mysqltest/mysqltest.go`
- Test: `internal/source/mysql/open_test.go`、`internal/source/mysql/probe_test.go`、`internal/source/mysql/integration_test.go`

- [ ] **Step 1: 锁定驱动**

本机模块缓存中没有 go-mysql，这一步需要访问 Go 模块代理下载一次（之后进入 `GOMODCACHE`，离线可用）：

Run: `GOPROXY=https://proxy.golang.org go get github.com/go-mysql-org/go-mysql@v1.16.0`
Expected: `go.mod` 增加 `github.com/go-mysql-org/go-mysql v1.16.0` 及其间接依赖，`go.sum` 由官方校验和数据库校验。若校验和失败：**停止并报告**，不要设置 `GONOSUMDB`、`GOFLAGS=-insecure` 等绕过。

- [ ] **Step 2: 写失败测试**

`internal/source/mysql/open_test.go`：

```go
package mysql

import (
	"strings"
	"testing"
)

func TestConnectorDisablesDriverRetries(t *testing.T) {
	c, err := connector("migrator:pw@127.0.0.1:3306/newapi?timeout=5s")
	if err != nil {
		t.Fatal(err)
	}
	// With retries on, go-mysql turns connection errors into ErrBadConn and
	// database/sql silently retries; a broken read must surface instead.
	if c.Params.Get("retries") != "off" || c.Params.Get("timeout") != "5s" || c.Addr != "127.0.0.1:3306" || c.DB != "newapi" {
		t.Fatalf("connector %+v", c)
	}
}

func TestOpenRejectsBadDSNWithoutEcho(t *testing.T) {
	_, err := Open("u:pw-secret-value@h:notaport/db")
	if err == nil || strings.Contains(err.Error(), "pw-secret-value") {
		t.Fatalf("bad DSN accepted or echoed: %v", err)
	}
}
```

`internal/source/mysql/probe_test.go`：

```go
package mysql

import (
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"
)

func TestCheckEnginesRequiresInnoDB(t *testing.T) {
	src := config.Source{Datasets: map[string]config.Dataset{
		"a": {Bindings: config.Bindings{Database: config.TableBinding{Table: "a"}}},
		"b": {Bindings: config.Bindings{Database: config.TableBinding{Table: "b"}}},
	}}
	r := checkEngines(src, contract.ProbeResult{}, map[string]string{"a": "innodb", "b": "MyISAM"})
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "b") || !strings.Contains(r.Problems[0], "InnoDB") {
		t.Fatalf("problems: %v", r.Problems)
	}
}
```

- [ ] **Step 3: 运行，确认失败**

Run: `go test ./internal/source/mysql/`
Expected: 编译失败，`undefined: connector`、`undefined: checkEngines`。

- [ ] **Step 4: 实现**

`internal/source/source.go`：

```go
// Package source is the boundary between readers and the transform/write
// stages. Readers are driven by a project's field list; nothing here is
// specific to New API.
package source

import (
	"context"

	"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"
)

// Start describes a full read: the binlog position recorded before it (G0;
// incremental sync starts there, design 9.4) and the instance it ran on.
type Start struct{ G0, ServerUUID string }

// FullReader reads every dataset in one consistent read and calls fn for
// each record. Any error means the read is unusable; start over.
type FullReader interface {
	Snapshot(ctx context.Context, fn func(contract.Record) error) (Start, error)
}

// Lookup reads records matching all predicates, ordered by primary key.
type Lookup interface {
	Find(dataset string, preds ...contract.Predicate) ([]contract.Record, error)
}

// Querier runs fn inside one consistent read on the pinned source instance.
type Querier interface {
	Query(ctx context.Context, fn func(Lookup) error) error
}
```

`internal/source/mysql/open.go`：

```go
// Package mysql reads a project's datasets from MySQL: full reads, lookups
// and the precheck for binlog subscription (design 9.2). All MySQL access in
// this project uses github.com/go-mysql-org/go-mysql.
package mysql

import (
	"database/sql"
	"errors"

	gomysql "github.com/go-mysql-org/go-mysql/driver"
)

// connector parses a go-mysql DSN (user:password@host:port/db?params) and
// turns driver retries off: a failed read must surface, not be retried on a
// different connection behind our back.
func connector(dsn string) (gomysql.Connector, error) {
	c, err := gomysql.ParseDSN(dsn)
	if err != nil || c.Addr == "" {
		return gomysql.Connector{}, errors.New("invalid MySQL DSN; expected user:password@host:port/db?params")
	}
	c.Params.Set("retries", "off")
	return c, nil
}

// Open returns a connection pool. Errors never include the DSN.
func Open(dsn string) (*sql.DB, error) {
	c, err := connector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(c), nil
}
```

go-mysql 驱动的值：整数为 `int64`/`uint64`，字符串、JSON、DATETIME 为文本字节，NULL 为 nil；`database/sql` 扫描到 `*[]byte` 时统一转成文本，正好交给任务 2 的类型转换。go-mysql 没有 `parseTime` 选项，DATETIME 本来就是文本。

`internal/source/mysql/probe.go`：

```go
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"
)

// ProbeDatabase compares the field list with the current schema's columns.
// Non-InnoDB tables block: consistent reads cover transactional engines only.
func ProbeDatabase(ctx context.Context, db *sql.DB, src config.Source) (contract.ProbeResult, error) {
	columns := map[string][]string{}
	err := scanPairs(ctx, db, "SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME, ORDINAL_POSITION",
		func(table, column string) { columns[table] = append(columns[table], column) })
	if err != nil {
		return contract.ProbeResult{}, errors.New("read source columns failed")
	}
	engines := map[string]string{}
	err = scanPairs(ctx, db, "SELECT TABLE_NAME, COALESCE(ENGINE, '') FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()",
		func(table, engine string) { engines[table] = engine })
	if err != nil {
		return contract.ProbeResult{}, errors.New("read source table engines failed")
	}
	return checkEngines(src, contract.Probe(src, columns), engines), nil
}

func scanPairs(ctx context.Context, db *sql.DB, query string, fn func(a, b string)) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }() // rows.Err is checked below
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return err
		}
		fn(a, b)
	}
	return rows.Err()
}

func checkEngines(src config.Source, r contract.ProbeResult, engines map[string]string) contract.ProbeResult {
	for _, name := range slices.Sorted(maps.Keys(src.Datasets)) {
		table := src.Datasets[name].Bindings.Database.Table
		if engine, ok := engines[table]; ok && !strings.EqualFold(engine, "InnoDB") {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: table %s uses engine %s; consistent reads require InnoDB", name, table, engine))
		}
	}
	return r
}
```

`internal/source/mysql/mysqltest/mysqltest.go`：

```go
//go:build integration

// Package mysqltest creates disposable MySQL databases for integration tests
// on the server named by MIGRATOR_TEST_MYSQL_DSN (go-mysql DSN form,
// user:password@host:port/). The account needs CREATE and DROP; the database
// in the DSN is never modified.
package mysqltest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/driver"
)

// IsolatedDatabase returns the DSN of a fresh database plus an admin handle
// on the server. Both are cleaned up when the test ends.
func IsolatedDatabase(t testing.TB) (dsn, name string, admin *sql.DB) {
	t.Helper()
	raw := os.Getenv("MIGRATOR_TEST_MYSQL_DSN")
	if raw == "" {
		t.Fatal("MIGRATOR_TEST_MYSQL_DSN is required for MySQL integration tests")
	}
	c, err := gomysql.ParseDSN(raw)
	if err != nil || c.Addr == "" {
		t.Fatal("test DSN must be a go-mysql DSN: user:password@host:port/")
	}
	admin = sql.OpenDB(c)
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name = fmt.Sprintf("migrator_src_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatal("create isolated test database failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("cleanup isolated test database failed")
		}
	})
	u := url.URL{Scheme: "mysql", User: url.UserPassword(c.User, c.Password), Host: c.Addr, Path: "/" + name, RawQuery: c.Params.Encode()}
	return u.String(), name, admin
}
```

`internal/source/mysql/integration_test.go`（本任务先放探测用例和公共造数函数，后面任务往里追加）：

```go
//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/mysql/mysqltest"
)

// A made-up project: the reader must work for any field list.
func integrationSource() config.Source {
	return config.Source{Datasets: map[string]config.Dataset{"items": {
		PrimaryKey: "id",
		Schema: map[string]config.Field{
			"id":       {Type: "int", Required: true},
			"secret":   {Type: "secret", Required: true},
			"group":    {Type: "string", Required: true},
			"status":   {Type: "int", Required: true},
			"options":  {Type: "json", Required: true, Nullable: true},
			"deleted":  {Type: "timestamp", Nullable: true},
		},
		Dropped:  []string{"used"},
		Bindings: config.Bindings{Database: config.TableBinding{Table: "items"}},
	}}}
}

const itemsDDL = "CREATE TABLE items (id bigint PRIMARY KEY AUTO_INCREMENT, `secret` text NOT NULL, `group` varchar(64) NOT NULL, status bigint NOT NULL, options text NULL, deleted datetime(3) NULL, used bigint NOT NULL DEFAULT 0, extra_col int NULL) ENGINE=InnoDB"

type fixture struct {
	db    *sql.DB
	admin *sql.DB
	name  string
}

// seed creates items 1..n; even ids are in groups "default,vip", odd in "default".
// options: id%3==1 → '' ; id%3==0 → NULL ; otherwise a JSON object.
func seed(t *testing.T, n int) fixture {
	t.Helper()
	dsn, name, admin := mysqltest.IsolatedDatabase(t)
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, itemsDDL); err != nil {
		t.Fatal("create items failed")
	}
	for i := 1; i <= n; i++ {
		group := "default"
		if i%2 == 0 {
			group = "default,vip"
		}
		var opts any = `{"a":"b"}`
		switch i % 3 {
		case 0:
			opts = nil
		case 1:
			opts = ""
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO items(id,`secret`,`group`,status,options) VALUES (?,?,?,?,?)", i, fmt.Sprintf("sk-%d", i), group, 1, opts); err != nil {
			t.Fatal("seed failed")
		}
	}
	return fixture{db: db, admin: admin, name: name}
}

func TestProbeDatabaseReportsColumnsAndEngine(t *testing.T) {
	f := seed(t, 0)
	ctx := context.Background()
	r, err := ProbeDatabase(ctx, f.db, integrationSource())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Blocking(); err != nil {
		t.Fatalf("unexpected block: %v", err)
	}
	if !slices.Equal(r.Unknown["items"], []string{"extra_col"}) {
		t.Fatalf("unknown columns: %v", r.Unknown["items"])
	}
	if _, err := f.db.ExecContext(ctx, "ALTER TABLE items ENGINE=MyISAM"); err != nil {
		t.Fatal("switch engine failed")
	}
	r, err = ProbeDatabase(ctx, f.db, integrationSource())
	if err != nil || r.Blocking() == nil || !strings.Contains(r.Blocking().Error(), "InnoDB") {
		t.Fatalf("MyISAM table not blocking: %v %v", err, r.Blocking())
	}
}
```

- [ ] **Step 5: 运行，确认通过**

Run: `go test -race ./internal/source/... && go vet -tags=integration ./internal/source/... && go test -tags=integration -run '^$' ./internal/source/...`
Expected: 单元测试 PASS；集成测试编译通过。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/source/
git commit -m "feat(source): MySQL connection and information_schema probe"
```

---

## 任务 6：全量读取

在一个连接、一个只读事务里读完全部表（design 3.3）。顺序：读所连实例的 `server_uuid` → 读当前 binlog 位置 G0 → `START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY` → 按数据集名顺序逐表分页 → `COMMIT`。成功后把该 `server_uuid` 记为“本任务的实例”，之后的回源查询都必须连到它。

失败时连接直接丢弃、不归还连接池（服务器在断开时回滚），避免带着未结束事务的连接被复用。

**Files:**
- Create: `internal/source/mysql/reader.go`
- Test: `internal/source/mysql/reader_test.go`、追加到 `internal/source/mysql/integration_test.go`

- [ ] **Step 1: 写失败测试**

`internal/source/mysql/reader_test.go`：

```go
package mysql

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"
)

// fakeSession serves keyset pages from in-memory tables of
// [id, name, secret, groups] rows. It ignores SQL filters on purpose:
// the reader must apply exact filtering itself.
type fakeSession struct {
	tables    map[string][][][]byte
	uuid      string
	execs     []string
	queries   []string
	args      [][]any
	gtids     []string
	failQuery int // 1-based query number whose rows report an error; 0 = never
	closed    bool
	discarded bool
}

func (s *fakeSession) exec(_ context.Context, q string) error { s.execs = append(s.execs, q); return nil }
func (s *fakeSession) gtidExecuted(context.Context) (string, error) {
	g := s.gtids[0]
	s.gtids = s.gtids[1:]
	return g, nil
}
func (s *fakeSession) serverUUID(context.Context) (string, error) { return s.uuid, nil }
func (s *fakeSession) close(discard bool)                         { s.closed, s.discarded = true, discard }
func (s *fakeSession) query(_ context.Context, q string, args ...any) (rowScanner, error) {
	s.queries = append(s.queries, q)
	s.args = append(s.args, args)
	cursor, limit := args[len(args)-2].(int64), args[len(args)-1].(int)
	var table string
	for name := range s.tables {
		if strings.Contains(q, "FROM `"+name+"`") {
			table = name
		}
	}
	var page [][][]byte
	for _, row := range s.tables[table] {
		id, _ := strconv.ParseInt(string(row[0]), 10, 64)
		if id > cursor && len(page) < limit {
			page = append(page, row)
		}
	}
	r := &fakeRows{data: page}
	if s.failQuery == len(s.queries) {
		r.err = errors.New("password=hunter2 broken pipe")
	}
	return r, nil
}

type fakeRows struct {
	data [][][]byte
	i    int
	err  error
}

func (r *fakeRows) Next() bool {
	if r.err != nil || r.i >= len(r.data) {
		return false
	}
	r.i++
	return true
}
func (r *fakeRows) Scan(dest ...any) error {
	for j, d := range dest {
		*(d.(*[]byte)) = r.data[r.i-1][j]
	}
	return nil
}
func (r *fakeRows) Err() error   { return r.err }
func (r *fakeRows) Close() error { return nil }

// rows builds ids 1..n; even ids belong to "default,vip", odd to "default".
func rows(n int) [][][]byte {
	var out [][][]byte
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		groups := "default"
		if i%2 == 0 {
			groups = "default,vip"
		}
		out = append(out, [][]byte{[]byte(id), []byte("n" + id), []byte("sk-" + id), []byte(groups)})
	}
	return out
}

func testSource() config.Source {
	ds := func(table string) config.Dataset {
		return config.Dataset{
			PrimaryKey: "id",
			Schema: map[string]config.Field{
				"id":     {Type: "int", Required: true},
				"name":   {Type: "string", Required: true},
				"secret": {Type: "secret", Required: true},
				"groups": {Type: "string", Required: true},
			},
			Bindings: config.Bindings{Database: config.TableBinding{Table: table}},
		}
	}
	return config.Source{Datasets: map[string]config.Dataset{"users": ds("users"), "items": ds("items")}}
}

func testReader(s *fakeSession, pageSize int) *Reader {
	src := testSource()
	// Column order matches the fake rows: id, name, secret, groups.
	cols := []string{"id", "name", "secret", "groups"}
	probe := contract.Probe(src, map[string][]string{"items": cols, "users": cols})
	for name := range probe.Present {
		probe.Present[name] = cols
	}
	return &Reader{
		contract: src,
		probe:    probe,
		pageSize: pageSize,
		open:     func(context.Context) (session, error) { return s, nil },
	}
}

func collect(t *testing.T, r *Reader) ([]contract.Record, error) {
	t.Helper()
	var got []contract.Record
	_, err := r.Snapshot(context.Background(), func(rec contract.Record) error { got = append(got, rec); return nil })
	return got, err
}

func TestSnapshotPagesWithoutGaps(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(7), "users": rows(6)}, uuid: "u1", gtids: []string{"g0"}}
	got, err := collect(t, testReader(s, 3))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, rec := range got {
		keys = append(keys, rec.Dataset+":"+rec.Key)
	}
	want := []string{"items:1", "items:2", "items:3", "items:4", "items:5", "items:6", "items:7",
		"users:1", "users:2", "users:3", "users:4", "users:5", "users:6"}
	if !slices.Equal(keys, want) {
		t.Fatalf("records %v", keys)
	}
	// items: pages 3,3,1 → 3 queries; users: 3,3,0 → 3 queries.
	if len(s.queries) != 6 {
		t.Fatalf("queries=%d", len(s.queries))
	}
}

func TestSnapshotTransactionProtocol(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(1), "users": rows(1)}, uuid: "uuid-a", gtids: []string{"uuid:1-5"}}
	r := testReader(s, 10)
	start, err := r.Snapshot(context.Background(), func(contract.Record) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY",
		"COMMIT",
	}
	if !slices.Equal(s.execs, want) || start.G0 != "uuid:1-5" || start.ServerUUID != "uuid-a" {
		t.Fatalf("execs=%v start=%+v", s.execs, start)
	}
	if !s.closed || s.discarded {
		t.Fatal("a successful read must return its connection to the pool")
	}
	if r.Server() != "uuid-a" {
		t.Fatal("full read did not pin the source instance")
	}
}

func TestSnapshotAbortsOnReadFailure(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(7), "users": rows(1)}, uuid: "u1", gtids: []string{"g0"}, failQuery: 2}
	got, err := collect(t, testReader(s, 3))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("failure not reported or driver error leaked: %v", err)
	}
	if slices.Contains(s.execs, "COMMIT") || !s.discarded {
		t.Fatalf("failed read committed or kept its connection: %v discarded=%v", s.execs, s.discarded)
	}
	if len(got) != 3 {
		t.Fatalf("expected only the first page before failure, got %d", len(got))
	}
}

func TestSnapshotStopsOnCallbackError(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(3), "users": rows(1)}, uuid: "u1", gtids: []string{"g0"}}
	stop := errors.New("transform failed")
	_, err := testReader(s, 10).Snapshot(context.Background(), func(contract.Record) error { return stop })
	if !errors.Is(err, stop) || slices.Contains(s.execs, "COMMIT") {
		t.Fatalf("callback error not propagated: %v", err)
	}
}

func TestSnapshotRejectsNonIncreasingKeys(t *testing.T) {
	bad := rows(3)
	bad[1], bad[2] = bad[2], bad[1] // 1,3,2
	s := &fakeSession{tables: map[string][][][]byte{"items": bad, "users": rows(1)}, uuid: "u1", gtids: []string{"g0"}}
	if _, err := collect(t, testReader(s, 10)); err == nil || !strings.Contains(err.Error(), "order") {
		t.Fatalf("out-of-order keys accepted: %v", err)
	}
}

func TestSnapshotRefusesBlockingProbe(t *testing.T) {
	s := &fakeSession{uuid: "u1", gtids: []string{"g0"}}
	r := testReader(s, 10)
	r.probe = contract.Probe(r.contract, map[string][]string{"items": {"id"}})
	if _, err := collect(t, r); err == nil || len(s.execs) != 0 {
		t.Fatalf("read started despite blocking probe: %v %v", err, s.execs)
	}
}

func TestSnapshotRejectsDifferentInstance(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(1), "users": rows(1)}, uuid: "other", gtids: []string{"g0"}}
	r := testReader(s, 10)
	r.Pin("pinned")
	if _, err := collect(t, r); !errors.Is(err, ErrServerChanged) || len(s.execs) != 0 {
		t.Fatalf("read ran on a different instance: %v %v", err, s.execs)
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/source/mysql/ -run Snapshot`
Expected: 编译失败，`undefined: Reader`、`undefined: session`、`undefined: ErrServerChanged`。

- [ ] **Step 3: 实现** `internal/source/mysql/reader.go`

```go
package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"
)

// ErrServerChanged means a connection reached a different MySQL instance
// than the one this job reads from. Binlog subscription, full reads and
// lookups must use one instance (design 9.2); the job must pause.
var ErrServerChanged = errors.New("source MySQL instance changed (server_uuid differs); point the DSN at one fixed instance")

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// session is one physical connection; the seam keeps paging testable offline.
type session interface {
	exec(ctx context.Context, query string) error
	query(ctx context.Context, query string, args ...any) (rowScanner, error)
	gtidExecuted(ctx context.Context) (string, error)
	serverUUID(ctx context.Context) (string, error)
	close(discard bool)
}

type sqlSession struct{ conn *sql.Conn }

func (s sqlSession) exec(ctx context.Context, q string) error {
	_, err := s.conn.ExecContext(ctx, q)
	return err
}
func (s sqlSession) query(ctx context.Context, q string, args ...any) (rowScanner, error) {
	rows, err := s.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
func (s sqlSession) gtidExecuted(ctx context.Context) (string, error) {
	var v sql.NullString
	if err := s.conn.QueryRowContext(ctx, "SELECT @@GLOBAL.gtid_executed").Scan(&v); err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(v.String), ""), nil
}
func (s sqlSession) serverUUID(ctx context.Context) (string, error) {
	var v string
	err := s.conn.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&v)
	return v, err
}

// close returns the connection to the pool, or discards it when it may still
// carry an open transaction; the server rolls back on disconnect.
func (s sqlSession) close(discard bool) {
	if discard {
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = s.conn.Close()
}

// Reader reads a project's datasets from one MySQL instance. It implements
// source.FullReader and source.Querier.
type Reader struct {
	contract config.Source
	probe    contract.ProbeResult
	pageSize int
	location *time.Location
	open     func(context.Context) (session, error)

	mu     sync.Mutex
	server string // pinned server_uuid; "" until a full read or Pin
}

var (
	_ source.FullReader = (*Reader)(nil)
	_ source.Querier    = (*Reader)(nil)
)

// NewReader builds a reader for a probed source. loc is the instance's
// DATETIME zone (config Access.Location).
func NewReader(db *sql.DB, src config.Source, probe contract.ProbeResult, pageSize int, loc *time.Location) (*Reader, error) {
	if db == nil || pageSize <= 0 {
		return nil, errors.New("source reader requires a database and a positive page size")
	}
	return &Reader{contract: src, probe: probe, pageSize: pageSize, location: loc,
		open: func(ctx context.Context) (session, error) {
			c, err := db.Conn(ctx)
			if err != nil {
				return nil, err
			}
			return sqlSession{c}, nil
		}}, nil
}

// Pin fixes the instance this reader must use (normally the precheck's
// server_uuid). A full read pins automatically when nothing is pinned.
func (r *Reader) Pin(serverUUID string) {
	r.mu.Lock()
	r.server = serverUUID
	r.mu.Unlock()
}

// Server returns the pinned server_uuid, or "".
func (r *Reader) Server() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.server
}

func (r *Reader) checkServer(uuid string) error {
	if pinned := r.Server(); pinned != "" && pinned != uuid {
		return ErrServerChanged
	}
	return nil
}

// Snapshot reads every dataset inside one consistent read on one connection.
func (r *Reader) Snapshot(ctx context.Context, fn func(contract.Record) error) (source.Start, error) {
	if err := r.probe.Blocking(); err != nil {
		return source.Start{}, err
	}
	s, err := r.open(ctx)
	if err != nil {
		return source.Start{}, errors.New("open source connection failed")
	}
	ok := false
	defer func() { s.close(!ok) }()
	uuid, err := s.serverUUID(ctx)
	if err != nil {
		return source.Start{}, errors.New("read source server_uuid failed")
	}
	if err := r.checkServer(uuid); err != nil {
		return source.Start{}, err
	}
	g0, err := s.gtidExecuted(ctx)
	if err != nil {
		return source.Start{}, errors.New("read source gtid_executed failed")
	}
	if err := beginConsistentRead(ctx, s); err != nil {
		return source.Start{}, err
	}
	for _, name := range slices.Sorted(maps.Keys(r.contract.Datasets)) {
		d := r.contract.Datasets[name]
		q, err := contract.PageQuery(contract.MySQL, d.Bindings.Database.Table, d.PrimaryKey, r.probe.Present[name])
		if err != nil {
			return source.Start{}, fmt.Errorf("dataset %s: %w", name, err)
		}
		if err := r.pageThrough(ctx, s, name, q, nil, fn); err != nil {
			return source.Start{}, err
		}
	}
	if err := s.exec(ctx, "COMMIT"); err != nil {
		return source.Start{}, errors.New("finish source read transaction failed")
	}
	ok = true
	if r.Server() == "" {
		r.Pin(uuid)
	}
	return source.Start{G0: g0, ServerUUID: uuid}, nil
}

func beginConsistentRead(ctx context.Context, s session) error {
	for _, q := range []string{
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY",
	} {
		if err := s.exec(ctx, q); err != nil {
			return errors.New("start source read transaction failed")
		}
	}
	return nil
}

// pageThrough runs a keyset-paginated query (args + cursor + limit) and calls
// fn for every normalized row in primary-key order.
func (r *Reader) pageThrough(ctx context.Context, s session, dataset, q string, args []any, fn func(contract.Record) error) error {
	d := r.contract.Datasets[dataset]
	cols := r.probe.Present[dataset]
	norm := contract.Normalizer{Dataset: dataset, Decl: d, Location: r.location}
	cursor := int64(math.MinInt64)
	for {
		pageArgs := append(append(make([]any, 0, len(args)+2), args...), cursor, r.pageSize)
		rows, err := s.query(ctx, q, pageArgs...)
		if err != nil {
			return fmt.Errorf("read dataset %s failed", dataset)
		}
		n, last, err := consume(rows, cols, norm, cursor, fn)
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("read dataset %s failed", dataset)
		}
		if err != nil {
			return err
		}
		if n < r.pageSize {
			return nil
		}
		cursor = last
	}
}

// consume counts every row read (for paging), including rows fn ignores.
func consume(rows rowScanner, cols []string, norm contract.Normalizer, cursor int64, fn func(contract.Record) error) (int, int64, error) {
	n := 0
	for rows.Next() {
		raw := make([][]byte, len(cols))
		dest := make([]any, len(cols))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return n, cursor, fmt.Errorf("read dataset %s failed", norm.Dataset)
		}
		rec, err := norm.Normalize(cols, raw)
		if err != nil {
			return n, cursor, err
		}
		key, _ := strconv.ParseInt(rec.Key, 10, 64)
		if key <= cursor {
			return n, cursor, fmt.Errorf("dataset %s: primary key order violated", norm.Dataset)
		}
		cursor = key
		n++
		if err := fn(rec); err != nil {
			return n, cursor, err
		}
	}
	if rows.Err() != nil {
		return n, cursor, fmt.Errorf("read dataset %s failed", norm.Dataset)
	}
	return n, cursor, nil
}
```

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/source/mysql/ -v`
Expected: 7 个 Snapshot 测试与任务 5 的测试全部 PASS。

- [ ] **Step 5: 追加真实 MySQL 测试**（追加到 `internal/source/mysql/integration_test.go`，只编译）

```go
func newReader(t *testing.T, f fixture, page int) *Reader {
	t.Helper()
	probe, err := ProbeDatabase(context.Background(), f.db, integrationSource())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(f.db, integrationSource(), probe, page, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSnapshotReadsAllRowsAcrossPages(t *testing.T) {
	f := seed(t, 25)
	r := newReader(t, f, 10)
	seen := map[string]contract.Record{}
	start, err := r.Snapshot(context.Background(), func(rec contract.Record) error { seen[rec.Key] = rec; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 25 || start.ServerUUID == "" || r.Server() != start.ServerUUID {
		t.Fatalf("rows=%d start=%+v", len(seen), start)
	}
	if seen["1"].Fields["options"] != nil || seen["3"].Fields["options"] != nil {
		t.Fatal("empty string / NULL JSON not normalized to nil")
	}
	if _, ok := seen["2"].Fields["options"].(map[string]any); !ok {
		t.Fatal("JSON object not decoded")
	}
	if seen["7"].Secrets["secret"].Reveal() != "sk-7" {
		t.Fatal("secret not read")
	}
}

func TestSnapshotIgnoresConcurrentCommits(t *testing.T) {
	f := seed(t, 5)
	r := newReader(t, f, 2)
	seen := map[string]contract.Record{}
	first := true
	_, err := r.Snapshot(context.Background(), func(rec contract.Record) error {
		if first {
			first = false
			for _, q := range []string{
				"INSERT INTO `" + f.name + "`.items(id,`secret`,`group`,status) VALUES (99,'sk-99','default',1)",
				"UPDATE `" + f.name + "`.items SET status=2 WHERE id=5",
			} {
				if _, err := f.admin.Exec(q); err != nil {
					t.Fatal("concurrent write failed")
				}
			}
		}
		seen[rec.Key] = rec
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["99"]; ok || seen["5"].Fields["status"] != int64(1) {
		t.Fatal("changes committed after the read started were visible")
	}
}

func TestSnapshotFailsWhenConnectionKilled(t *testing.T) {
	f := seed(t, 10)
	r := newReader(t, f, 2)
	calls := 0
	_, err := r.Snapshot(context.Background(), func(contract.Record) error {
		calls++
		if calls == 3 {
			killConnections(t, f)
		}
		return nil
	})
	if err == nil {
		t.Fatal("killed read connection not reported")
	}
}

func killConnections(t *testing.T, f fixture) {
	t.Helper()
	rows, err := f.admin.Query("SELECT ID FROM information_schema.PROCESSLIST WHERE DB = ? AND ID <> CONNECTION_ID()", f.name)
	if err != nil {
		t.Fatal("list connections failed")
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		_, _ = f.admin.Exec(fmt.Sprintf("KILL %d", id))
	}
}
```

在该文件 import 中加入 `"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"`。

Run: `go vet -tags=integration ./internal/source/... && go test -tags=integration -run '^$' ./internal/source/...`
Expected: 编译通过。

- [ ] **Step 6: 提交**

```bash
git add internal/source/mysql/
git commit -m "feat(source): full read of all datasets in one consistent read"
```

---

## 任务 7：按条件回源查询

写入器重算资源时调用 `Query`：在一个只读一致性读事务里执行若干次 `Find`，保证几次查询看到的是同一时刻的数据（design 9.6）。每次 `Query` 都先确认所连实例的 `server_uuid` 与已固定的实例相同；不同则返回 `ErrServerChanged`，由服务暂停任务（design 9.2）。尚未固定实例时拒绝查询。

`Find` 校验条件（`CheckPredicates`）→ 生成 SQL 粗筛 → 按主键分页 → Go 中精确过滤（`MatchesAll`）。

**Files:**
- Modify: `internal/source/mysql/reader.go`
- Test: 追加到 `internal/source/mysql/reader_test.go`、`internal/source/mysql/integration_test.go`

- [ ] **Step 1: 写失败测试**（追加到 `reader_test.go`）

```go
func pinnedReader(s *fakeSession, page int) *Reader {
	r := testReader(s, page)
	r.Pin(s.uuid)
	return r
}

func TestQueryProtocol(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(4)}, uuid: "u1"}
	err := pinnedReader(s, 10).Query(context.Background(), func(l source.Lookup) error {
		_, err := l.Find("items", contract.In("id", int64(1)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY",
		"COMMIT",
	}
	if !slices.Equal(s.execs, want) || s.discarded {
		t.Fatalf("execs=%v discarded=%v", s.execs, s.discarded)
	}
}

func TestFindFiltersExactly(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(6)}, uuid: "u1"}
	var byName, vip []contract.Record
	err := pinnedReader(s, 10).Query(context.Background(), func(l source.Lookup) error {
		var err error
		if byName, err = l.Find("items", contract.In("name", "n2", "n4")); err != nil {
			return err
		}
		vip, err = l.Find("items", contract.ListContains("groups", "vip", ","))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := func(rs []contract.Record) (out []string) {
		for _, r := range rs {
			out = append(out, r.Key)
		}
		return out
	}
	if !slices.Equal(keys(byName), []string{"2", "4"}) || !slices.Equal(keys(vip), []string{"2", "4", "6"}) {
		t.Fatalf("byName=%v vip=%v", keys(byName), keys(vip))
	}
	if !strings.Contains(s.queries[0], "`name` IN (?,?)") || !strings.Contains(s.queries[1], "`groups` LIKE ? ESCAPE '!'") {
		t.Fatalf("queries %v", s.queries)
	}
	if vip[0].Secrets["secret"].Reveal() != "sk-2" {
		t.Fatal("lookup must return secret values for the writer")
	}
}

func TestFindPagesThroughLargeResults(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(7)}, uuid: "u1"}
	var got []contract.Record
	err := pinnedReader(s, 2).Query(context.Background(), func(l source.Lookup) error {
		var err error
		got, err = l.Find("items", contract.ListContains("groups", "default", ","))
		return err
	})
	if err != nil || len(got) != 7 || len(s.queries) != 4 {
		t.Fatalf("records=%d queries=%d err=%v", len(got), len(s.queries), err)
	}
}

func TestFindRejectsBadPredicates(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(1)}, uuid: "u1"}
	for name, p := range map[string]contract.Predicate{
		"secret":        contract.In("secret", "sk-1"),
		"type mismatch": contract.In("id", "1"),
		"unknown field": contract.In("nope", "x"),
	} {
		err := pinnedReader(s, 10).Query(context.Background(), func(l source.Lookup) error {
			_, err := l.Find("items", p)
			return err
		})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	err := pinnedReader(s, 10).Query(context.Background(), func(l source.Lookup) error {
		_, err := l.Find("missing_dataset")
		return err
	})
	if err == nil {
		t.Error("unknown dataset accepted")
	}
}

func TestFindEmptyInSkipsQuery(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(3)}, uuid: "u1"}
	var got []contract.Record
	err := pinnedReader(s, 10).Query(context.Background(), func(l source.Lookup) error {
		var err error
		got, err = l.Find("items", contract.In("id"))
		return err
	})
	if err != nil || len(got) != 0 || len(s.queries) != 0 {
		t.Fatalf("got=%d queries=%d err=%v", len(got), len(s.queries), err)
	}
}

func TestQueryRequiresSameInstance(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(1)}, uuid: "replica-b"}
	r := testReader(s, 10)
	if err := r.Query(context.Background(), func(source.Lookup) error { return nil }); err == nil {
		t.Fatal("query ran before an instance was pinned")
	}
	r.Pin("replica-a")
	ran := false
	err := r.Query(context.Background(), func(source.Lookup) error { ran = true; return nil })
	if !errors.Is(err, ErrServerChanged) || ran || !s.discarded || slices.Contains(s.execs, "COMMIT") {
		t.Fatalf("query on another instance: err=%v ran=%v discarded=%v", err, ran, s.discarded)
	}
}

func TestQueryDiscardsConnectionOnError(t *testing.T) {
	s := &fakeSession{tables: map[string][][][]byte{"items": rows(1)}, uuid: "u1"}
	boom := errors.New("writer failed")
	err := pinnedReader(s, 10).Query(context.Background(), func(source.Lookup) error { return boom })
	if !errors.Is(err, boom) || !s.discarded || slices.Contains(s.execs, "COMMIT") {
		t.Fatalf("err=%v discarded=%v execs=%v", err, s.discarded, s.execs)
	}
}
```

在 `reader_test.go` 的 import 中加入 `"github.com/hyperbolic-c/newapi2ACG/internal/source"`。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/source/mysql/ -run 'Query|Find'`
Expected: 编译失败，`r.Query undefined`。

- [ ] **Step 3: 实现**（追加到 `reader.go`）

```go
// Query runs fn inside one consistent read on the pinned instance. The
// connection is discarded on any error, returned to the pool on success.
func (r *Reader) Query(ctx context.Context, fn func(source.Lookup) error) error {
	if err := r.probe.Blocking(); err != nil {
		return err
	}
	if r.Server() == "" {
		return errors.New("source instance not pinned; run the precheck or a full read first")
	}
	s, err := r.open(ctx)
	if err != nil {
		return errors.New("open source connection failed")
	}
	ok := false
	defer func() { s.close(!ok) }()
	uuid, err := s.serverUUID(ctx)
	if err != nil {
		return errors.New("read source server_uuid failed")
	}
	if err := r.checkServer(uuid); err != nil {
		return err
	}
	if err := beginConsistentRead(ctx, s); err != nil {
		return err
	}
	if err := fn(&lookup{ctx: ctx, r: r, s: s}); err != nil {
		return err
	}
	if err := s.exec(ctx, "COMMIT"); err != nil {
		return errors.New("finish source read transaction failed")
	}
	ok = true
	return nil
}

type lookup struct {
	ctx context.Context
	r   *Reader
	s   session
}

func (l *lookup) Find(dataset string, preds ...contract.Predicate) ([]contract.Record, error) {
	d, ok := l.r.contract.Datasets[dataset]
	if !ok {
		return nil, fmt.Errorf("datasets.%s: not declared in the field list", dataset)
	}
	cols := l.r.probe.Present[dataset]
	if err := contract.CheckPredicates(dataset, d, cols, preds); err != nil {
		return nil, err
	}
	q, args, empty, err := contract.LookupQuery(contract.MySQL, d.Bindings.Database.Table, d.PrimaryKey, cols, preds)
	if err != nil {
		return nil, fmt.Errorf("dataset %s: %w", dataset, err)
	}
	if empty {
		return nil, nil
	}
	var out []contract.Record
	err = l.r.pageThrough(l.ctx, l.s, dataset, q, args, func(rec contract.Record) error {
		if contract.MatchesAll(preds, rec) {
			out = append(out, rec)
		}
		return nil
	})
	return out, err
}
```

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/source/mysql/ -v`
Expected: 新增 7 个测试与已有测试全部 PASS。

- [ ] **Step 5: 追加真实 MySQL 测试**（追加到 `integration_test.go`，只编译）

```go
func TestQueryFindsByListAndPins(t *testing.T) {
	f := seed(t, 9)
	r := newReader(t, f, 3)
	if _, err := r.Snapshot(context.Background(), func(contract.Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var vip, odd []contract.Record
	err := r.Query(context.Background(), func(l source.Lookup) error {
		var err error
		if vip, err = l.Find("items", contract.ListContains("group", "vip", ",")); err != nil {
			return err
		}
		odd, err = l.Find("items", contract.In("id", int64(1), int64(3), int64(5)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(vip) != 4 || len(odd) != 3 {
		t.Fatalf("vip=%d odd=%d", len(vip), len(odd))
	}
	r.Pin("00000000-0000-0000-0000-000000000000")
	if err := r.Query(context.Background(), func(source.Lookup) error { return nil }); !errors.Is(err, ErrServerChanged) {
		t.Fatalf("different pinned instance not detected: %v", err)
	}
}
```

在该文件 import 中加入 `"errors"` 和 `"github.com/hyperbolic-c/newapi2ACG/internal/source"`。

Run: `go vet -tags=integration ./internal/source/... && go test -tags=integration -run '^$' ./internal/source/...`
Expected: 编译通过。

- [ ] **Step 6: 提交**

```bash
git add internal/source/mysql/
git commit -m "feat(source): consistent lookups pinned to one MySQL instance"
```

---

## 任务 8：源库预检

检查所连实例能否同时支撑全量读取、回源查询和 binlog 订阅（design 9.2），任一不满足都给出具体原因：

- 字段清单与表结构（任务 5 的 `ProbeDatabase`）。
- `log_bin=ON`、`binlog_format=ROW`、`binlog_row_image=FULL`、`gtid_mode=ON`。
- 所连实例是从库时，`log_replica_updates`（旧版本 `log_slave_updates`）必须为 ON，否则 New API 的变更不会出现在它的 binlog 里。
- 同时打开两条连接比较 `@@server_uuid`，不同说明连接地址是负载均衡地址，拒绝。（两条连接也可能碰巧落在同一台，所以运行中每次回源查询还会再校验，见任务 7。）
- 账号缺少 REPLICATION CLIENT 权限时（无法判断是否从库）报告为问题。REPLICATION SLAVE 权限要到真正订阅 binlog 时才能验证，留给 binlog 增量同步阶段。
- binlog 保留时间 `binlog_expire_logs_seconds` 只记录在结果里，阈值判断留给 binlog 增量同步阶段。

**Files:**
- Create: `internal/source/mysql/precheck.go`
- Test: `internal/source/mysql/precheck_test.go`、追加到 `internal/source/mysql/integration_test.go`

- [ ] **Step 1: 写失败测试** `internal/source/mysql/precheck_test.go`

```go
package mysql

import (
	"strings"
	"testing"
)

func goodVars() map[string]string {
	return map[string]string{"log_bin": "ON", "binlog_format": "ROW", "binlog_row_image": "FULL", "gtid_mode": "ON", "log_replica_updates": "ON"}
}

func TestEvaluateAcceptsGoodPrimaryAndReplica(t *testing.T) {
	for _, replica := range []bool{false, true} {
		if p := evaluate(goodVars(), replica, []string{"u1", "u1"}); len(p) != 0 {
			t.Fatalf("replica=%v problems %v", replica, p)
		}
	}
}

func TestEvaluateReportsEachSetting(t *testing.T) {
	for name, bad := range map[string]string{"log_bin": "OFF", "binlog_format": "MIXED", "binlog_row_image": "MINIMAL", "gtid_mode": "OFF_PERMISSIVE"} {
		vars := goodVars()
		vars[name] = bad
		p := evaluate(vars, false, []string{"u1", "u1"})
		if len(p) != 1 || !strings.Contains(p[0], name) {
			t.Errorf("%s=%s: problems %v", name, bad, p)
		}
	}
}

func TestEvaluateReplicaNeedsReplicaUpdates(t *testing.T) {
	vars := goodVars()
	vars["log_replica_updates"] = "OFF"
	if p := evaluate(vars, false, []string{"u1", "u1"}); len(p) != 0 {
		t.Fatalf("primary does not need log_replica_updates: %v", p)
	}
	if p := evaluate(vars, true, []string{"u1", "u1"}); len(p) != 1 || !strings.Contains(p[0], "log_replica_updates") {
		t.Fatalf("replica without log_replica_updates accepted: %v", p)
	}
	// Older servers only expose log_slave_updates.
	delete(vars, "log_replica_updates")
	vars["log_slave_updates"] = "ON"
	if p := evaluate(vars, true, []string{"u1", "u1"}); len(p) != 0 {
		t.Fatalf("log_slave_updates fallback ignored: %v", p)
	}
}

func TestEvaluateRejectsDifferentInstances(t *testing.T) {
	p := evaluate(goodVars(), false, []string{"u1", "u2"})
	if len(p) != 1 || !strings.Contains(p[0], "load-balanced") {
		t.Fatalf("different server_uuid accepted: %v", p)
	}
	if p := evaluate(goodVars(), false, []string{"", ""}); len(p) == 0 {
		t.Fatal("missing server_uuid accepted")
	}
}

func TestPrecheckResultBlocking(t *testing.T) {
	r := PrecheckResult{Problems: []string{"log_bin must be ON"}}
	if err := r.Blocking(); err == nil || !strings.Contains(err.Error(), "log_bin") {
		t.Fatalf("blocking %v", err)
	}
	if err := (PrecheckResult{}).Blocking(); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/source/mysql/ -run 'Evaluate|PrecheckResult'`
Expected: 编译失败，`undefined: evaluate`、`undefined: PrecheckResult`。

- [ ] **Step 3: 实现** `internal/source/mysql/precheck.go`

```go
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	gmerrors "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/contract"
)

// PrecheckResult is what the source precheck found (design 9.2).
type PrecheckResult struct {
	Probe                  contract.ProbeResult
	ServerUUID             string
	Replica                bool
	BinlogRetentionSeconds int64 // reported; thresholds belong to binlog sync
	Problems               []string
}

// Blocking returns an error when the job must stay in PRECHECK.
func (r PrecheckResult) Blocking() error {
	var parts []string
	if err := r.Probe.Blocking(); err != nil {
		parts = append(parts, err.Error())
	}
	parts = append(parts, r.Problems...)
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("source precheck failed: %s", strings.Join(parts, "; "))
}

var precheckVariables = []string{"log_bin", "binlog_format", "binlog_row_image", "gtid_mode", "log_replica_updates", "log_slave_updates", "binlog_expire_logs_seconds"}

// Precheck checks that the configured instance can serve full reads, lookups
// and binlog subscription. It needs a pool that can open two connections.
func Precheck(ctx context.Context, db *sql.DB, src config.Source) (PrecheckResult, error) {
	probe, err := ProbeDatabase(ctx, db, src)
	if err != nil {
		return PrecheckResult{}, err
	}
	first, err := db.Conn(ctx)
	if err != nil {
		return PrecheckResult{}, errors.New("open source connection failed")
	}
	defer func() { _ = first.Close() }()
	second, err := db.Conn(ctx) // held concurrently: a distinct physical connection
	if err != nil {
		return PrecheckResult{}, errors.New("open second source connection failed")
	}
	defer func() { _ = second.Close() }()
	var uuids []string
	for _, c := range []*sql.Conn{first, second} {
		var u string
		if err := c.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&u); err != nil {
			return PrecheckResult{}, errors.New("read source server_uuid failed")
		}
		uuids = append(uuids, u)
	}
	vars, err := readVariables(ctx, first)
	if err != nil {
		return PrecheckResult{}, err
	}
	replica, privilegeProblem, err := isReplica(ctx, first)
	if err != nil {
		return PrecheckResult{}, err
	}
	res := PrecheckResult{Probe: probe, ServerUUID: uuids[0], Replica: replica}
	res.BinlogRetentionSeconds, _ = strconv.ParseInt(vars["binlog_expire_logs_seconds"], 10, 64)
	res.Problems = evaluate(vars, replica, uuids)
	if privilegeProblem != "" {
		res.Problems = append(res.Problems, privilegeProblem)
	}
	return res, nil
}

// readVariables inlines the names: they are constants above, and SHOW cannot
// reliably be sent as a prepared statement (which bound arguments would cause).
func readVariables(ctx context.Context, c *sql.Conn) (map[string]string, error) {
	q := "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('" + strings.Join(precheckVariables, "','") + "')"
	rows, err := c.QueryContext(ctx, q)
	if err != nil {
		return nil, errors.New("read source server variables failed")
	}
	defer func() { _ = rows.Close() }() // rows.Err is checked below
	vars := map[string]string{}
	for rows.Next() {
		var name, value string
		if rows.Scan(&name, &value) != nil {
			return nil, errors.New("read source server variables failed")
		}
		vars[strings.ToLower(name)] = value
	}
	if rows.Err() != nil {
		return nil, errors.New("read source server variables failed")
	}
	return vars, nil
}

// isReplica uses SHOW REPLICA STATUS (MySQL 8.0.22+), falling back to SHOW
// SLAVE STATUS. A privilege error is reported as a problem, not a failure.
func isReplica(ctx context.Context, c *sql.Conn) (bool, string, error) {
	for _, q := range []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"} {
		rows, err := c.QueryContext(ctx, q)
		if err != nil {
			var me *gmerrors.MyError
			if errors.As(err, &me) {
				switch me.Code {
				case 1064: // syntax: older server, try the legacy statement
					continue
				case 1227: // access denied
					return false, "account lacks REPLICATION CLIENT privilege; cannot tell whether the instance is a replica", nil
				}
			}
			return false, "", errors.New("read source replication status failed")
		}
		replica := rows.Next()
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return false, "", errors.New("read source replication status failed")
		}
		return replica, "", nil
	}
	return false, "", errors.New("read source replication status failed")
}

// evaluate turns server settings into human-readable problems (design 9.2).
func evaluate(vars map[string]string, replica bool, uuids []string) []string {
	var problems []string
	want := func(name, expect string) {
		if !strings.EqualFold(vars[name], expect) {
			problems = append(problems, fmt.Sprintf("%s must be %s (found %q)", name, expect, vars[name]))
		}
	}
	want("log_bin", "ON")
	want("binlog_format", "ROW")
	want("binlog_row_image", "FULL")
	want("gtid_mode", "ON")
	if replica {
		v, ok := vars["log_replica_updates"]
		if !ok {
			v = vars["log_slave_updates"]
		}
		if !strings.EqualFold(v, "ON") {
			problems = append(problems, "instance is a replica: log_replica_updates must be ON so source changes reach its binlog")
		}
	}
	for _, u := range uuids {
		if u == "" {
			return append(problems, "server_uuid unavailable")
		}
	}
	for _, u := range uuids[1:] {
		if u != uuids[0] {
			return append(problems, "connections reached different MySQL instances (server_uuid differs); point the DSN at one fixed instance, not a load-balanced endpoint")
		}
	}
	return problems
}
```

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/source/mysql/ -v`
Expected: 新增 5 个测试与已有测试全部 PASS。

- [ ] **Step 5: 追加真实 MySQL 测试**（追加到 `integration_test.go`，只编译）

```go
func TestPrecheckReadsServerSettings(t *testing.T) {
	f := seed(t, 1)
	res, err := Precheck(context.Background(), f.db, integrationSource())
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerUUID == "" {
		t.Fatal("server_uuid not read")
	}
	// The test server's binlog settings are not under our control, so only
	// check that every reported problem names a setting or privilege.
	for _, p := range res.Problems {
		if !strings.ContainsAny(p, "_") {
			t.Errorf("unexplained problem %q", p)
		}
	}
}
```

Run: `go vet -tags=integration ./internal/source/... && go test -tags=integration -run '^$' ./internal/source/...`
Expected: 编译通过。

- [ ] **Step 6: 跑全部检查并提交**

```sh
gofmt -l cmd internal test
go vet ./... && go vet -tags=integration ./...
go test -race -count=1 ./...
go test -tags=integration -run '^$' ./...
golangci-lint run ./... && golangci-lint run --build-tags=integration ./...
make leak-scan
uv run --no-project --with-requirements test/schema/requirements.txt python test/schema/check.py
```
Expected: 全部通过；lint `0 issues.`；`no leaks found`；Schema `PASS`。

```bash
git add internal/source/mysql/
git commit -m "feat(source): MySQL precheck for full read, lookups and binlog"
```

---

## 任务 9：记录进度

- [ ] **Step 1:** `docs/implementation-status.md`：
  - 总览表“读取：从 MySQL 读全量、按条件回源查询”改为“代码完成，未连真实库”，说明三项能力和“读取层只依赖字段清单，New API 通过 `configs/sources/new-api.yaml` 接入”。
  - “最近一次检查结果”更新为本次结果。
  - 列出只编译未运行的 MySQL 测试：`TestProbeDatabaseReportsColumnsAndEngine`、`TestSnapshotReadsAllRowsAcrossPages`、`TestSnapshotIgnoresConcurrentCommits`、`TestSnapshotFailsWhenConnectionKilled`、`TestQueryFindsByListAndPins`、`TestPrecheckReadsServerSettings`，运行方式 `MIGRATOR_TEST_MYSQL_DSN='user:pass@host:3306/' go test -tags=integration ./internal/source/...`（go-mysql 地址格式）。
  - 已知限制：REPLICATION SLAVE 权限与 binlog 保留时间阈值留给 binlog 增量同步阶段；两条连接比较 `server_uuid` 不能百分之百识别负载均衡地址，运行中由每次查询的校验兜底。
  - 下一步：编写“转换：规则引擎与 New API 映射规则”的计划。
- [ ] **Step 2:** `README.md`“当前进度”增加：从 MySQL 全量读取、按条件回源查询、源库预检（尚未接入 `migrator run`）；“开发与测试”补充 `MIGRATOR_TEST_MYSQL_DSN`（go-mysql 地址格式 `user:pass@host:3306/`）；“依赖”说明 MySQL 访问统一使用 go-mysql。
- [ ] **Step 3:** 提交并推送

```bash
git add docs/implementation-status.md README.md
git commit -m "docs: record MySQL read progress"
git push
```

---

## 自检记录

- 覆盖设计文档：3.1（项目字段清单与实例连接分离；新增实例时区）、3.2（显式列、保留字加引号、按主键分页、类型转换、只读事务）、3.3（一个事务读完所有表、中断即报错重读）、3.4（缺必需列阻止、未知列告警）、9.2（同一实例、`server_uuid` 校验、binlog 相关配置与从库要求、InnoDB）、9.4（读取前记录 G0）、9.6（按条件回源查询、一致性读、返回密钥原文供写入）、11.2（`FullReader`、`Querier`、`Lookup` 接口）。
- 通用性：读取层代码不出现任何 New API 表名或字段名；测试使用虚构的 `items` / `users` 数据集；New API 仅通过任务 4 的配置测试验证。
- 留给后续：binlog 订阅与 REPLICATION SLAVE 验证、保留时间阈值（binlog 增量同步）；G0 写入状态库、`Pin` 由预检结果设置、接入 `migrator run`（写入：全量写入 ACG）；PostgreSQL / SQLite / 管理 API 读取（定期核对与运维阶段）。
- 依赖：MySQL 访问统一用 go-mysql（SQL 走其 `driver` 包，binlog 走其 `replication` 包），不引入 `go-sql-driver/mysql`。
- 名称一致性：`contract.Record`、`contract.Predicate`、`source.Start`、`source.FullReader`、`source.Querier`、`source.Lookup`、`mysql.Reader`（`Snapshot`、`Query`、`Pin`、`Server`）、`mysql.ErrServerChanged`、`mysql.Precheck` 在各任务中一致。
