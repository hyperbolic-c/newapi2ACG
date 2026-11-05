package event

import "fmt"

type PositionMode string

const (
	ModeGTID    PositionMode = "gtid"
	ModeFilePos PositionMode = "filepos"
)

// SourcePosition is a point in the source change log (design-core §5.3).
// GTID mode is preferred; file/position is kept as well when known.
type SourcePosition struct {
	Mode       PositionMode `json:"mode"`
	GTIDSet    string       `json:"gtid_set,omitempty"`
	BinlogFile string       `json:"binlog_file,omitempty"`
	BinlogPos  uint64       `json:"binlog_pos,omitempty"`
}

func (p SourcePosition) IsZero() bool { return p.Mode == "" }

func (p SourcePosition) String() string {
	switch p.Mode {
	case ModeGTID:
		return "gtid:" + p.GTIDSet
	case ModeFilePos:
		return fmt.Sprintf("%s:%d", p.BinlogFile, p.BinlogPos)
	}
	return "none"
}

// Cursor is a primary-key paging cursor inside one snapshot session. It is
// only meaningful within that session and is never persisted (design-core §8.2).
type Cursor struct {
	Started bool
	Value   any
}
