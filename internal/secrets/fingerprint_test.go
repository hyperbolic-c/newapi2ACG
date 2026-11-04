package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestFingerprintSalted(t *testing.T) {
	t.Setenv("TEST_MIGRATOR_SALT_A", "fixture-salt-a")
	t.Setenv("TEST_MIGRATOR_SALT_B", "fixture-salt-b")
	a, err := NewFingerprinter("TEST_MIGRATOR_SALT_A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFingerprinter("TEST_MIGRATOR_SALT_B")
	if err != nil {
		t.Fatal(err)
	}
	secret := New("fixture-private-value")
	x := a.Sum(secret)
	if x != a.Sum(secret) || x == b.Sum(secret) {
		t.Fatal("fingerprint is not stable and salted")
	}
	mac := hmac.New(sha256.New, []byte("fixture-salt-a"))
	mac.Write([]byte(secret.Reveal()))
	if x != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("not HMAC-SHA256 hex")
	}
	if got := ShortFingerprint(x); got != x[:8] {
		t.Fatalf("unexpected report fingerprint %q", got)
	}
}

func TestFingerprintRequiresSalt(t *testing.T) {
	t.Setenv("TEST_MIGRATOR_EMPTY_SALT", "")
	for _, name := range []string{"", "TEST_MIGRATOR_EMPTY_SALT"} {
		if _, err := NewFingerprinter(name); err == nil {
			t.Fatal("accepted missing salt")
		}
	}
}

func TestShortFingerprintRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"", "short", "this-is-a-secret-not-a-fingerprint"} {
		if got := ShortFingerprint(input); got != "***" {
			t.Fatal("report exposed malformed fingerprint")
		}
	}
}

func TestFingerprinterFormattingDoesNotExposeSalt(t *testing.T) {
	t.Setenv("TEST_FINGERPRINT_PRIVATE_SALT", "fixture-private-fingerprint-salt")
	fingerprinter, err := NewFingerprinter("TEST_FINGERPRINT_PRIVATE_SALT")
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, fingerprinter), "fixture-private-fingerprint-salt") {
			t.Errorf("format %s exposed fingerprint salt", format)
		}
	}
}
