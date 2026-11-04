package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretFormatRedacted(t *testing.T) {
	secret := New("fixture-private-value")
	for _, format := range []string{"%v", "%s", "%+v", "%#v", "%q", "%x", "%20s"} {
		if got := fmt.Sprintf(format, secret); got != "***" {
			t.Errorf("format %s was not redacted", format)
		}
	}
	if secret.Reveal() != "fixture-private-value" {
		t.Fatal("Reveal did not return original")
	}
}

func TestSecretJSONRedacted(t *testing.T) {
	data, err := json.Marshal(struct {
		Key Value `json:"key"`
	}{New("fixture-private-value")})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"key":"***"}` {
		t.Fatal("JSON did not redact secret")
	}
}

func TestSecretSlogRedacted(t *testing.T) {
	for _, useJSON := range []bool{false, true} {
		var out bytes.Buffer
		var handler slog.Handler = slog.NewTextHandler(&out, nil)
		if useJSON {
			handler = slog.NewJSONHandler(&out, nil)
		}
		slog.New(handler).Info("test", "key", New("fixture-private-value"))
		if strings.Contains(out.String(), "fixture-private-value") || !strings.Contains(out.String(), "***") {
			t.Fatal("slog did not redact secret")
		}
	}
}

func TestSecretErrorWrapRedacted(t *testing.T) {
	base := errors.New("write failed")
	err := fmt.Errorf("%w: %v", base, New("fixture-private-value"))
	if !errors.Is(err, base) || strings.Contains(err.Error(), "fixture-private-value") || !strings.Contains(err.Error(), "***") {
		t.Fatal("error did not preserve cause and redact secret")
	}
}

func TestSecretCannotBeRestoredFromRedactedJSON(t *testing.T) {
	var value Value
	if err := json.Unmarshal([]byte(`"***"`), &value); err == nil {
		t.Fatal("redacted value must not become a writable secret")
	}
}
