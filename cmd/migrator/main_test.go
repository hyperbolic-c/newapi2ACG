package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestValidateCommand(t *testing.T) {
	var out, errs bytes.Buffer
	code := execute(context.Background(), []string{"validate", "--config-dir", "../../configs"}, &out, &errs)
	if code != 0 || !strings.Contains(out.String(), "config_hash") || errs.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errs)
	}
}
func TestValidateMissingDirectory(t *testing.T) {
	var out, errs bytes.Buffer
	if code := execute(context.Background(), []string{"validate", "--config-dir", t.TempDir() + "/absent"}, &out, &errs); code == 0 || errs.Len() == 0 {
		t.Fatal("missing configuration accepted")
	}
}
func TestUnknownCommand(t *testing.T) {
	var out, errs bytes.Buffer
	for _, args := range [][]string{nil, {"apply"}} {
		if code := execute(context.Background(), args, &out, &errs); code != 2 {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
}
