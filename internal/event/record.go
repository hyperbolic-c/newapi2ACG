// Package event is the protocol between data collection and the processing
// pipeline (design-core §3). Collectors (full read, binlog, reconciliation)
// produce events; mapping and writing never see where they came from.
package event

// Operation distinguishes a snapshot state from a change. A snapshot record is
// "this row exists with this content", not "this row was inserted".
type Operation string

const (
	OpSnapshot Operation = "snapshot"
	OpInsert   Operation = "insert"
	OpUpdate   Operation = "update"
	OpDelete   Operation = "delete"
)

// Record is one source row. Data holds column values normalized by the source
// (integers int64, text string, NULL nil, ...). Columns configured as
// sensitive hold secrets.Value. Records must never be logged.
type Record struct {
	Dataset string
	Key     string
	Data    map[string]any
}
