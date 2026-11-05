package event

import "testing"

func TestPositionString(t *testing.T) {
	cases := map[string]SourcePosition{
		"none":                {},
		"gtid:uuid:1-5":       {Mode: ModeGTID, GTIDSet: "uuid:1-5"},
		"binlog.000003:12345": {Mode: ModeFilePos, BinlogFile: "binlog.000003", BinlogPos: 12345},
	}
	for want, p := range cases {
		if got := p.String(); got != want {
			t.Errorf("%+v: got %q want %q", p, got, want)
		}
	}
	if !(SourcePosition{}).IsZero() || (SourcePosition{Mode: ModeGTID}).IsZero() {
		t.Fatal("IsZero wrong")
	}
}
