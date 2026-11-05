package event

// ChangeEvent is one row-level fact. Before is set for update and delete,
// After for snapshot, insert and update.
type ChangeEvent struct {
	SourceID  string
	Operation Operation
	Before    *Record
	After     *Record
	Position  SourcePosition
}

// ChangeBatch is the unit of confirmation. For binlog batches it is exactly
// one source transaction and EndPosition is the position after its commit:
// the checkpoint may advance to EndPosition only after the whole batch is
// applied. Snapshot batches have a zero EndPosition: a snapshot cursor is
// never a committed binlog position (design-core §3.1).
type ChangeBatch struct {
	Events      []ChangeEvent
	EndPosition SourcePosition
}
