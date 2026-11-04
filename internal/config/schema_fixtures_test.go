package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestConfigContractFixtures(t *testing.T) {
	data, err := os.ReadFile("../../test/fixtures/configs/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Schema string
		Valid        bool
		Config       json.RawMessage
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	categories := map[string]string{"source": "sources", "instance": "instances", "job": "jobs", "target": "targets"}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			// JSON documents are a YAML subset. Both validators use the same fixtures.
			filename := categories[tc.Schema] + "/source.yaml"
			if tc.Schema == "instance" {
				filename = "instances/prod.yaml"
			}
			_, err := LoadFiles(map[string][]byte{filename: tc.Config})
			if (err == nil) != tc.Valid {
				t.Fatalf("expected valid=%v got %v", tc.Valid, err)
			}
		})
	}
}
