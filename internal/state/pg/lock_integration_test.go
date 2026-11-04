//go:build integration

package pg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOnlyOneLeaseHolder(t *testing.T) {
	_, dsn := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	leases := make([]*Lease, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range leases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			leases[i], errs[i] = AcquireLease(ctx, dsn, "same-job", LeaseOptions{})
		}(i)
	}
	close(start)
	wg.Wait()
	winners, busy := 0, 0
	for i, l := range leases {
		if l != nil {
			winners++
			defer func() { _ = l.Close() }()
		} else if errors.Is(errs[i], ErrLeaseHeld) {
			busy++
		} else {
			t.Fatal("unexpected lease acquisition error")
		}
	}
	if winners != 1 || busy != 1 {
		t.Fatal("expected exactly one lease holder")
	}
	other, err := AcquireLease(ctx, dsn, "different-job", LeaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestLeaseReleasedOnClose(t *testing.T) {
	_, dsn := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := AcquireLease(ctx, dsn, "job", LeaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLease(ctx, dsn, "job", LeaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
}
func TestLeaseLostCancelsContext(t *testing.T) {
	db, dsn := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := LeaseOptions{CheckInterval: 20 * time.Millisecond, CheckTimeout: 100 * time.Millisecond}
	lease, err := AcquireLease(ctx, dsn, "job", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	key := uint64(leaseKey("job"))
	var pid int
	err = db.QueryRowContext(ctx, `SELECT pid FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=$1::oid AND objid=$2::oid AND objsubid=1 AND granted`, int64(key>>32), int64(key&0xffffffff)).Scan(&pid)
	if err != nil {
		t.Fatal("could not identify lease session")
	}
	var terminated bool
	if err := db.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("could not terminate lease session")
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(opts.CheckInterval + opts.CheckTimeout + time.Second):
		t.Fatal("lease loss not detected within bounded check window")
	}
	if !errors.Is(context.Cause(lease.Context()), ErrLeaseLost) {
		t.Fatal("wrong lease loss cause")
	}
	replacement, err := AcquireLease(ctx, dsn, "job", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}
