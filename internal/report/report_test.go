package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/test/contract"
)

func TestReportDeterministic(t *testing.T) {
	a := contract.Group("group:a", "a")
	b := contract.Group("group:b", "b")
	x, err := Render([]model.Resource{b, a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	y, err := Render([]model.Resource{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x, y) {
		t.Fatal("input order changed report")
	}
}
func TestReportFingerprintRedacted(t *testing.T) {
	full := strings.Repeat("a", 64)
	r := model.Resource{APIVersion: "acg/v1", Kind: model.ProviderKind, Metadata: model.Metadata{SourceIdentity: model.Identity{Instance: "source", Kind: model.ProviderKind, Local: "channel:1"}, MigrationJob: "job"}, Provider: &model.Provider{Name: "p", Type: "openai", Endpoint: "https://example.test", Enabled: true, APIKeys: []model.SecretRef{{Fingerprint: full}}}}
	data, err := Render([]model.Resource{r}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(full)) || !bytes.Contains(data, []byte(`"fingerprint": "aaaaaaaa"`)) {
		t.Fatal("fingerprint not shortened")
	}
	if r.Provider.APIKeys[0].Fingerprint != full {
		t.Fatal("report mutated original")
	}
}
