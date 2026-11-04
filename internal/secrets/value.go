// Package secrets provides explicitly revealed, otherwise redacted secrets.
package secrets

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
)

const redacted = "***"

// Value keeps a secret out of ordinary formatting, logging and serialization.
// This is a serialization boundary, not a memory-encryption mechanism. Callers
// must never log Reveal's result or put it in persisted resource fields.
type Value struct {
	raw string
}

func New(raw string) Value { return Value{raw: raw} }

// Reveal is intended only for source conversion and target request construction.
func (v Value) Reveal() string { return v.raw }

func (Value) String() string   { return redacted }
func (Value) GoString() string { return redacted }

func (Value) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redacted)
}

func (Value) MarshalJSON() ([]byte, error) { return []byte(`"***"`), nil }
func (Value) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Value) LogValue() slog.Value         { return slog.StringValue(redacted) }

// Deserialization must not turn a report's placeholder into a usable credential.
func (*Value) UnmarshalJSON([]byte) error {
	return errors.New("secret cannot be restored from serialized data; reread the source")
}

func (*Value) UnmarshalText([]byte) error {
	return errors.New("secret cannot be restored from serialized data; reread the source")
}
