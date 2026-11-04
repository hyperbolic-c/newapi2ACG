// Package leak scans artifacts generated during this run. PostgreSQL scanning
// belongs to the future state-store integration gate, not this offline test.
package leak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/report"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
	filetarget "github.com/hyperbolic-c/newapi2ACG/internal/target/file"
	"github.com/hyperbolic-c/newapi2ACG/test/contract"
)

func containsSecret(data []byte, values []string) bool {
	for _, value := range values {
		if bytes.Contains(data, []byte(value)) {
			return true
		}
	}
	return false
}

func TestScannerDetectsInjectedLeak(t *testing.T) {
	if !containsSecret([]byte(`{"unexpected":"fixture-provider-private"}`), []string{"fixture-provider-private"}) {
		t.Fatal("scanner missed injected leak")
	}
}

func TestGeneratedArtifactsContainNoSecrets(t *testing.T) {
	values := []string{"fixture-provider-private", "fixture-hmac-salt-private"}
	t.Setenv("TEST_LEAK_SALT", values[1])
	fp, err := secrets.NewFingerprinter("TEST_LEAK_SALT")
	if err != nil {
		t.Fatal(err)
	}
	secret := secrets.New(values[0])
	resource := model.Resource{APIVersion: "acg/v1", Kind: model.ProviderKind, Metadata: model.Metadata{SourceIdentity: model.Identity{Instance: "source", Kind: model.ProviderKind, Local: "channel:1"}, MigrationJob: "job"}, Provider: &model.Provider{Name: "provider", Type: "openai", Endpoint: "https://example.test", Enabled: true, APIKeys: []model.SecretRef{{Fingerprint: fp.Sum(secret)}}}}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("fixture", "secret", secret, "fingerprinter", fp)
	fmt.Fprintf(&logs, "%v %+v %#v", fp, fp, fp)
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := report.Render([]model.Resource{resource}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	adapter := filetarget.New(path)
	results, err := adapter.Apply(context.Background(), []model.Change{contract.Change("provider-create", resource)})
	if err != nil || len(results) != 1 || results[0].Outcome != model.Succeeded {
		t.Fatalf("apply failed: %v", err)
	}
	observed, err := adapter.Observe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"logs": logs.Bytes(), "secret-json": encoded, "report": rendered, "file": output, "observe": observation} {
		if containsSecret(data, values) {
			t.Errorf("secret leaked into %s", name)
		}
	}
	if bytes.Contains(output, []byte(fp.Sum(secret))) {
		t.Fatal("file report contains full fingerprint")
	}
}
