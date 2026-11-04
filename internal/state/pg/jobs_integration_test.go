//go:build integration

package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/service"
)

func TestJobRepositoryPersistsAcrossInstances(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	first := NewJobRepository(db)
	if err := first.Ensure(ctx, "job", map[string]string{"target": "memory-test"}, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := first.Load(ctx, "job"); err != nil || got.State != service.Precheck {
		t.Fatalf("new job not in PRECHECK: %+v %v", got, err)
	}
	want := service.JobState{State: service.Paused, ResumeFrom: service.Streaming, Reason: "manual pause"}
	if err := first.Save(ctx, "job", want); err != nil {
		t.Fatal(err)
	}
	second := NewJobRepository(db)
	if err := second.Ensure(ctx, "job", nil, "hash-2"); err != nil {
		t.Fatal(err)
	}
	got, err := second.Load(ctx, "job")
	if err != nil || got != want {
		t.Fatalf("state not persisted: %+v %v", got, err)
	}
	var hash string
	if err := db.QueryRow("SELECT config_hash FROM migrator.jobs WHERE job_id='job'").Scan(&hash); err != nil || hash != "hash-1" {
		t.Fatal("restart overwrote config_hash")
	}
}
func TestJobRepositoryRejectsCorruptPersistedState(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO migrator.jobs(job_id,state) VALUES ('job','PAUSED')"); err != nil {
		t.Fatal("insert job failed")
	}
	if _, err := NewJobRepository(db).Load(ctx, "job"); err == nil {
		t.Fatal("paused job without resume_from accepted")
	}
	if _, err := NewJobRepository(db).Load(ctx, "absent"); !errors.Is(err, ErrJobNotFound) {
		t.Fatal("missing job not reported")
	}
}
