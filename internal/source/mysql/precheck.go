package mysql

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Check verifies everything full read, binlog subscription and
// reconciliation need on the one configured instance (design-core §5.4).
func (s *Source) Check(ctx context.Context) error {
	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	f, err := s.gather(ctx, c)
	if err != nil {
		return err
	}
	problems, warnings := s.evaluate(f)
	for _, w := range warnings {
		s.log.Warn("source precheck warning", "detail", w)
	}
	if len(problems) > 0 {
		return fmt.Errorf("source precheck failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

type column struct {
	name, dataType string
}

type facts struct {
	vars       map[string]string
	replica    bool
	replicaIDs []string          // server ids of replicas connected to this instance
	tables     map[string]string // table → engine
	columns    map[string][]column
	grants     string
}

var checkedVariables = []string{"log_bin", "binlog_format", "binlog_row_image", "binlog_row_metadata",
	"binlog_row_value_options", "gtid_mode", "log_replica_updates", "log_slave_updates", "server_id"}

func (s *Source) gather(ctx context.Context, c conn) (facts, error) {
	f := facts{vars: map[string]string{}, tables: map[string]string{}, columns: map[string][]column{}}
	// Names are constants; inlined because SHOW is not reliably preparable.
	r, err := execute(ctx, c, "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('"+strings.Join(checkedVariables, "','")+"')")
	if err != nil {
		return f, fmt.Errorf("read server variables failed: %s", describe(err))
	}
	for i := 0; i < r.RowNumber(); i++ {
		name, _ := r.GetString(i, 0)
		value, _ := r.GetString(i, 1)
		f.vars[strings.ToLower(name)] = value
	}
	for _, q := range []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"} {
		r, err := execute(ctx, c, q)
		if errorCode(err) == 1064 {
			continue
		}
		if err != nil {
			return f, fmt.Errorf("read replication status failed (needs REPLICATION CLIENT): %s", describe(err))
		}
		f.replica = r.Resultset != nil && r.RowNumber() > 0
		break
	}
	for _, q := range []string{"SHOW REPLICAS", "SHOW SLAVE HOSTS"} {
		r, err := execute(ctx, c, q)
		if errorCode(err) == 1064 {
			continue
		}
		if err == nil && r.Resultset != nil {
			for i := 0; i < r.RowNumber(); i++ {
				if id, err := r.GetStringByName(i, "Server_id"); err == nil {
					f.replicaIDs = append(f.replicaIDs, id)
				}
			}
		}
		break
	}
	r, err = execute(ctx, c, "SELECT TABLE_NAME, COALESCE(ENGINE,'') FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()")
	if err != nil {
		return f, fmt.Errorf("read tables failed: %s", describe(err))
	}
	for i := 0; i < r.RowNumber(); i++ {
		name, _ := r.GetString(i, 0)
		engine, _ := r.GetString(i, 1)
		f.tables[name] = engine
	}
	r, err = execute(ctx, c, "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME, ORDINAL_POSITION")
	if err != nil {
		return f, fmt.Errorf("read columns failed: %s", describe(err))
	}
	for i := 0; i < r.RowNumber(); i++ {
		table, _ := r.GetString(i, 0)
		name, _ := r.GetString(i, 1)
		typ, _ := r.GetString(i, 2)
		f.columns[table] = append(f.columns[table], column{name: name, dataType: strings.ToLower(typ)})
	}
	if r, err := execute(ctx, c, "SHOW GRANTS"); err == nil && r.Resultset != nil {
		var all []string
		for i := 0; i < r.RowNumber(); i++ {
			g, _ := r.GetString(i, 0)
			all = append(all, strings.ToUpper(g))
		}
		f.grants = strings.Join(all, "\n")
	}
	return f, nil
}

// evaluate turns gathered facts into blocking problems and warnings.
func (s *Source) evaluate(f facts) (problems, warnings []string) {
	want := func(name, expect, why string) {
		if !strings.EqualFold(f.vars[name], expect) {
			problems = append(problems, fmt.Sprintf("%s must be %s (found %q): %s", name, expect, f.vars[name], why))
		}
	}
	want("log_bin", "ON", "binlog subscription needs the binary log")
	want("binlog_format", "ROW", "row events are required")
	want("binlog_row_image", "FULL", "complete before/after rows are required")
	want("binlog_row_metadata", "FULL", "column names must travel with each row event")
	if v := f.vars["binlog_row_value_options"]; v != "" {
		problems = append(problems, fmt.Sprintf("binlog_row_value_options must be empty (found %q): partial JSON updates are not supported", v))
	}
	if s.opts.PositionMode == "gtid" {
		want("gtid_mode", "ON", "position_mode gtid is configured")
	}
	if f.replica {
		v, ok := f.vars["log_replica_updates"]
		if !ok {
			v = f.vars["log_slave_updates"]
		}
		if !strings.EqualFold(v, "ON") {
			problems = append(problems, "the source is a replica: log_replica_updates must be ON so upstream changes reach its binlog")
		}
	}
	own := fmt.Sprint(s.opts.ServerID)
	if f.vars["server_id"] == own {
		problems = append(problems, "replication.server_id equals the source's own server_id")
	}
	if slices.Contains(f.replicaIDs, own) {
		warnings = append(warnings, "a replica with the configured server_id is already connected (a previous run of this task, or a conflict)")
	}
	for _, d := range s.opts.Datasets {
		engine, ok := f.tables[d.Table]
		if !ok {
			problems = append(problems, fmt.Sprintf("dataset %s: table %s not found in %s", d.Name, d.Table, s.db))
			continue
		}
		if !strings.EqualFold(engine, "InnoDB") {
			problems = append(problems, fmt.Sprintf("dataset %s: table %s uses %s; consistent snapshots need InnoDB", d.Name, d.Table, engine))
		}
		var names []string
		for _, col := range f.columns[d.Table] {
			names = append(names, col.name)
			switch col.dataType {
			case "enum", "set", "bit":
				problems = append(problems, fmt.Sprintf("dataset %s: column %s has type %s, which snapshot and binlog encode differently (not supported yet)", d.Name, col.name, col.dataType))
			}
		}
		if !slices.Contains(names, d.PrimaryKey) {
			problems = append(problems, fmt.Sprintf("dataset %s: primary key column %s not found", d.Name, d.PrimaryKey))
		}
		for _, sc := range d.SensitiveColumns {
			if !slices.Contains(names, sc) {
				problems = append(problems, fmt.Sprintf("dataset %s: sensitive column %s not found", d.Name, sc))
			}
		}
	}
	if f.grants != "" && !strings.Contains(f.grants, "ALL PRIVILEGES") {
		for _, p := range []string{"RELOAD", "REPLICATION SLAVE", "REPLICATION CLIENT"} {
			if !strings.Contains(f.grants, p) {
				warnings = append(warnings, fmt.Sprintf("grants do not show %s (needed for snapshots and binlog); roles may still grant it", p))
			}
		}
	}
	return problems, warnings
}
