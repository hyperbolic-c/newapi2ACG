package pg

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/service"
)

type fakeJobs struct {
	rows      map[string]jobRow
	hashes    map[string]string
	fail      bool
	updates   int
	lastSaved jobRow
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{rows: map[string]jobRow{}, hashes: map[string]string{}}
}
func (f *fakeJobs) insert(_ context.Context, job string, _ []byte, hash string) error {
	if f.fail {
		return errors.New("password=private")
	}
	if _, ok := f.rows[job]; !ok {
		f.rows[job] = jobRow{state: "PRECHECK"}
		f.hashes[job] = hash
	}
	return nil
}
func (f *fakeJobs) load(_ context.Context, job string) (jobRow, error) {
	if f.fail {
		return jobRow{}, errors.New("password=private")
	}
	row, ok := f.rows[job]
	if !ok {
		return jobRow{}, sql.ErrNoRows
	}
	return row, nil
}
func (f *fakeJobs) update(_ context.Context, job string, row jobRow) (int64, error) {
	if f.fail {
		return 0, errors.New("password=private")
	}
	f.updates++
	if _, ok := f.rows[job]; !ok {
		return 0, nil
	}
	f.rows[job] = row
	f.lastSaved = row
	return 1, nil
}

func TestJobRepositoryEnsureDoesNotResetExisting(t *testing.T) {
	f := newFakeJobs()
	r := &JobRepository{sql: f}
	ctx := context.Background()
	if err := r.Ensure(ctx, "job", map[string]string{"a": "b"}, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, "job", service.JobState{State: service.Streaming}); err != nil {
		t.Fatal(err)
	}
	if err := r.Ensure(ctx, "job", nil, "hash-2"); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load(ctx, "job")
	if err != nil || got.State != service.Streaming || f.hashes["job"] != "hash-1" {
		t.Fatalf("restart reset job: %+v hash=%s err=%v", got, f.hashes["job"], err)
	}
}
func TestJobRepositoryLoadMissing(t *testing.T) {
	r := &JobRepository{sql: newFakeJobs()}
	if _, err := r.Load(context.Background(), "absent"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("missing job: %v", err)
	}
	if err := r.Save(context.Background(), "absent", service.JobState{State: service.Precheck}); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("save to missing job: %v", err)
	}
}
func TestJobRepositoryRejectsCorruptRow(t *testing.T) {
	f := newFakeJobs()
	f.rows["job"] = jobRow{state: "PAUSED"} // paused without resume_from
	if _, err := (&JobRepository{sql: f}).Load(context.Background(), "job"); err == nil {
		t.Fatal("corrupt persisted state accepted")
	}
}
func TestJobRepositorySaveValidatesBeforeWrite(t *testing.T) {
	f := newFakeJobs()
	f.rows["job"] = jobRow{state: "PRECHECK"}
	r := &JobRepository{sql: f}
	if err := r.Save(context.Background(), "job", service.JobState{State: service.Paused}); err == nil || f.updates != 0 {
		t.Fatal("invalid state written")
	}
	if err := r.Save(context.Background(), "job", service.JobState{State: service.Paused, ResumeFrom: service.Snapshotting, Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	if f.lastSaved != (jobRow{state: "PAUSED", resumeFrom: "SNAPSHOTTING", reason: "manual"}) {
		t.Fatalf("unexpected row %+v", f.lastSaved)
	}
}
func TestJobRepositoryErrorsSanitized(t *testing.T) {
	f := newFakeJobs()
	f.fail = true
	r := &JobRepository{sql: f}
	ctx := context.Background()
	for _, err := range []error{
		r.Ensure(ctx, "job", nil, ""),
		func() error { _, err := r.Load(ctx, "job"); return err }(),
		r.Save(ctx, "job", service.JobState{State: service.Precheck}),
	} {
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("driver error leaked or swallowed: %v", err)
		}
	}
}
