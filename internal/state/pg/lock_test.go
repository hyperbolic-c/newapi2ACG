package pg

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLeaseSession struct {
	acquired bool
	fail     bool
	closed   atomic.Int32
	probes   atomic.Int32
	block    bool
}

func (f *fakeLeaseSession) tryLock(context.Context, int64) (bool, error) { return f.acquired, nil }
func (f *fakeLeaseSession) ping(ctx context.Context) error {
	f.probes.Add(1)
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.fail {
		return errors.New("private DSN")
	}
	return nil
}
func (f *fakeLeaseSession) close() error { f.closed.Add(1); return nil }
func testLeaseOptions() LeaseOptions {
	return LeaseOptions{CheckInterval: time.Millisecond, CheckTimeout: 10 * time.Millisecond}
}
func TestLeaseBusyClosesSession(t *testing.T) {
	s := &fakeLeaseSession{}
	l, err := acquireLease(context.Background(), s, "job", testLeaseOptions())
	if !errors.Is(err, ErrLeaseHeld) || l != nil || s.closed.Load() != 1 {
		t.Fatal("busy session leaked")
	}
}
func TestLeaseHealthFailureCancels(t *testing.T) {
	for _, block := range []bool{false, true} {
		s := &fakeLeaseSession{acquired: true, fail: true, block: block}
		l, err := acquireLease(context.Background(), s, "job", testLeaseOptions())
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-l.Context().Done():
		case <-time.After(time.Second):
			t.Fatal("lost lease not canceled")
		}
		if !errors.Is(context.Cause(l.Context()), ErrLeaseLost) {
			t.Fatal("missing sanitized loss cause")
		}
		_ = l.Close()
		if s.closed.Load() != 1 {
			t.Fatal("session not closed exactly once")
		}
	}
}
func TestLeaseConcurrentClose(t *testing.T) {
	s := &fakeLeaseSession{acquired: true}
	l, err := acquireLease(context.Background(), s, "job", testLeaseOptions())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.Close() }()
	}
	wg.Wait()
	if l.Context().Err() == nil || s.closed.Load() != 1 {
		t.Fatal("close not idempotent")
	}
}
func TestLeaseParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &fakeLeaseSession{acquired: true}
	l, err := acquireLease(ctx, s, "job", testLeaseOptions())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation leaked session")
	}
	if s.closed.Load() != 1 {
		t.Fatal("session not closed")
	}
}
func TestLeaseKeyStableAndScoped(t *testing.T) {
	first, again := leaseKey("a"), leaseKey("a")
	if first != again || first == leaseKey("b") || first == 684739128603217 {
		t.Fatal("invalid lock key")
	}
}

func TestLeaseRejectsInvalidOptionsAndJob(t *testing.T) {
	for _, tc := range []struct {
		job     string
		options LeaseOptions
	}{{"", LeaseOptions{}}, {"job", LeaseOptions{CheckInterval: -time.Second}}, {"job", LeaseOptions{CheckTimeout: -time.Second}}} {
		s := &fakeLeaseSession{acquired: true}
		if l, err := acquireLease(context.Background(), s, tc.job, tc.options); err == nil || l != nil || s.closed.Load() != 1 {
			t.Fatal("invalid lease request retained session")
		}
	}
}
func TestLeaseCanceledAcquisitionClosesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &fakeLeaseSession{acquired: true}
	if l, err := acquireLease(ctx, s, "job", testLeaseOptions()); err == nil || l != nil || s.closed.Load() != 1 {
		t.Fatal("canceled acquisition retained lock")
	}
}

type stuckLeaseSession struct {
	fakeLeaseSession
	unblock chan struct{}
	once    sync.Once
}

func (s *stuckLeaseSession) ping(context.Context) error {
	<-s.unblock
	return errors.New("connection closed")
}
func (s *stuckLeaseSession) close() error {
	s.once.Do(func() { close(s.unblock) })
	return s.fakeLeaseSession.close()
}
func TestLeaseTimeoutCancelsStuckProbe(t *testing.T) {
	s := &stuckLeaseSession{fakeLeaseSession: fakeLeaseSession{acquired: true}, unblock: make(chan struct{})}
	l, err := acquireLease(context.Background(), s, "job", testLeaseOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.once.Do(func() { close(s.unblock) })
	select {
	case <-l.Context().Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("stuck driver prevented lease timeout")
	}
	done := make(chan struct{})
	go func() { _ = l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close blocked behind probe")
	}
}

func TestLeaseDialerCloseInterruptsSocket(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	d := &leaseDialer{sockets: []net.Conn{local}}
	done := make(chan error, 1)
	go func() { _, err := local.Read(make([]byte, 1)); done <- err }()
	d.close()
	d.close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("socket read survived shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("socket read remained blocked")
	}
	if _, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, ErrLeaseClosed) {
		t.Fatal("closed dialer attempted reconnect")
	}
}

type expiredLeaseSession struct{ fakeLeaseSession }

func (s *expiredLeaseSession) tryLock(ctx context.Context, _ int64) (bool, error) {
	<-ctx.Done()
	return true, nil
}
func TestLeaseRejectsSuccessAfterDeadline(t *testing.T) {
	s := &expiredLeaseSession{}
	if l, err := acquireLease(context.Background(), s, "job", testLeaseOptions()); err == nil || l != nil || s.closed.Load() != 1 {
		t.Fatal("expired acquisition returned active lease")
	}
}
