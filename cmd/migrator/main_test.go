package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const example = "../../configs/jobs/newapi-to-acg.yaml"

func TestValidate(t *testing.T) {
	var out, errs bytes.Buffer
	if code := execute(context.Background(), []string{"validate", "--config", example}, &out, &errs, func(string) string { return "" }); code != 0 {
		t.Fatalf("code %d: %s", code, &errs)
	}
	if !strings.Contains(out.String(), `"config_hash"`) || !strings.Contains(out.String(), `"tokens"`) {
		t.Fatalf("output %s", &out)
	}
}

func TestRunWithoutEnvironmentFailsWithVariableName(t *testing.T) {
	var out, errs bytes.Buffer
	code := execute(context.Background(), []string{"run", "--config", example}, &out, &errs, func(string) string { return "" })
	if code != 1 || !strings.Contains(errs.String(), "NEWAPI_MYSQL_DSN") {
		t.Fatalf("code %d: %s", code, &errs)
	}
}

func TestUsage(t *testing.T) {
	var out, errs bytes.Buffer
	for _, args := range [][]string{nil, {"bogus", "--config", example}, {"validate", "--config", example, "extra"}} {
		if code := execute(context.Background(), args, &out, &errs, func(string) string { return "" }); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
	}
}
