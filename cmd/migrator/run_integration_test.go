//go:build integration

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/state/pg"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/pg/pgtest"
)

// TestRunEmptyJob is the plan's Task 1.11 gate: run registers the job, holds
// the lease, persists PRECHECK with its reason, and releases the lease on
// shutdown so another instance can take over.
func TestRunEmptyJob(t *testing.T) {
	db, dsn := pgtest.IsolatedDatabase(t)
	env := defaultRunEnv()
	env.getenv = func(name string) string {
		if name == "MIGRATOR_STATE_DSN" {
			return dsn
		}
		return ""
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- runCommand(ctx, []string{"--config-dir", "../../configs", "--work-retry", "1h"}, logs, env)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		var state, reason string
		err := db.QueryRow("SELECT state, coalesce(reason,'') FROM migrator.jobs WHERE job_id='example'").Scan(&state, &reason)
		if err == nil && state == "PRECHECK" && reason == precheckPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach PRECHECK with reason; logs: %s", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if l, err := pg.AcquireLease(context.Background(), dsn, "example", pg.LeaseOptions{}); !errors.Is(err, pg.ErrLeaseHeld) {
		if l != nil {
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
		}
		t.Fatalf("second instance acquired a held lease: %v", err)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("shutdown exit %d; logs: %s", code, logs)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit on shutdown")
	}

	l, err := pg.AcquireLease(context.Background(), dsn, "example", pg.LeaseOptions{})
	if err != nil {
		t.Fatalf("lease not released after shutdown: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
