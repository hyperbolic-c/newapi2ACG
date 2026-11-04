package model

import (
	"encoding/json"
	"os"
	"testing"
)

// The independent schema gate uses exactly these fixtures. This Go test checks
// only the model side; it does not substitute for a JSON Schema validator.
func TestResourceContractFixtures(t *testing.T) {
	data, err := os.ReadFile("../../test/fixtures/resources/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string          `json:"name"`
		Valid    bool            `json:"valid"`
		Resource json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := Decode(tc.Resource)
			if (err == nil) != tc.Valid {
				t.Fatalf("expected valid=%v, got error=%v", tc.Valid, err)
			}
		})
	}
}
