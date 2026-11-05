package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
)

func loadExample(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../configs/jobs/newapi-to-acg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBuildReportsMissingEnvironmentByName(t *testing.T) {
	cfg := loadExample(t)
	env := map[string]string{"NEWAPI_MYSQL_DSN": "u:pw-secret@127.0.0.1:3306/newapi"}
	_, err := Build(context.Background(), cfg, func(k string) string { return env[k] }, slog.New(slog.DiscardHandler), observability.NewMetrics())
	if err == nil || !strings.Contains(err.Error(), "MIGRATOR_HASH_KEY") || strings.Contains(err.Error(), "pw-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestACGTargetNotYetAvailable(t *testing.T) {
	cfg := loadExample(t)
	cfg.Target.Type = "acg-api"
	if _, err := newTarget(cfg, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err=%v", err)
	}
}
