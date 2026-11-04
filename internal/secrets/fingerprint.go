package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// Fingerprinter uses a fixed, externally managed HMAC key. Changing the key
// changes every fingerprint, so it must remain stable across process restarts.
type Fingerprinter struct {
	salt Value
}

// Format protects the nested private salt from fmt's reflective struct output.
func (Fingerprinter) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redacted)
}

func NewFingerprinter(saltEnv string) (*Fingerprinter, error) {
	if saltEnv == "" {
		return nil, errors.New("fingerprint salt environment variable name is required")
	}
	salt, ok := os.LookupEnv(saltEnv)
	if !ok || salt == "" {
		return nil, errors.New("fingerprint salt environment variable is unset or empty")
	}
	return &Fingerprinter{salt: New(salt)}, nil
}

func (f *Fingerprinter) Sum(value Value) string {
	mac := hmac.New(sha256.New, []byte(f.salt.Reveal()))
	_, _ = mac.Write([]byte(value.Reveal()))
	return hex.EncodeToString(mac.Sum(nil))
}

// ShortFingerprint is for reports only; state and equality use the full digest.
// Malformed input is redacted rather than accidentally disclosing a raw secret.
func ShortFingerprint(fingerprint string) string {
	if len(fingerprint) != sha256.Size*2 {
		return redacted
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return redacted
	}
	return fingerprint[:8]
}
