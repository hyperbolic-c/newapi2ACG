package pg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

var (
	ErrLeaseHeld   = errors.New("job lease is already held")
	ErrLeaseLost   = errors.New("job lease connection lost")
	ErrLeaseClosed = errors.New("job lease closed")
)

// LeaseOptions bounds each health check. Zero values select defaults.
// Detection takes at most CheckInterval + CheckTimeout after connection loss.
type LeaseOptions struct{ CheckInterval, CheckTimeout time.Duration }

func (o LeaseOptions) normalized() (LeaseOptions, error) {
	if o.CheckInterval < 0 || o.CheckTimeout < 0 {
		return o, errors.New("lease durations must not be negative")
	}
	if o.CheckInterval == 0 {
		o.CheckInterval = time.Second
	}
	if o.CheckTimeout == 0 {
		o.CheckTimeout = time.Second
	}
	return o, nil
}

// Lease owns one physical PG session. Context cancellation is authoritative;
// Close cancels work and waits until that session is destroyed. A canceled
// lease must never be reused or silently reconnected.
type Lease struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	done     chan struct{}
	closeErr error
}

func (l *Lease) Context() context.Context { return l.ctx }
func (l *Lease) Close() error             { l.cancel(ErrLeaseClosed); <-l.done; return l.closeErr }

type leaseSession interface {
	tryLock(context.Context, int64) (bool, error)
	ping(context.Context) error
	close() error
}
type sqlLeaseSession struct {
	db      *sql.DB
	conn    *sql.Conn
	network *leaseDialer
}

func (s *sqlLeaseSession) tryLock(ctx context.Context, key int64) (bool, error) {
	stop := context.AfterFunc(ctx, s.network.close)
	defer stop()
	var held bool
	err := s.conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&held)
	return held, err
}
func (s *sqlLeaseSession) ping(ctx context.Context) error {
	var one int
	return s.conn.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}
func (s *sqlLeaseSession) close() error {
	s.network.close()
	// Returning ErrBadConn discards the physical session instead of putting an
	// advisory-lock-bearing connection back into a pool. Closing the owned DB
	// also prevents future reconnects. Session destruction releases the lock.
	_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = s.conn.Close()
	return s.db.Close()
}

// AcquireLease opens a separate single-connection DB, never borrowing from the
// state store pool. DSN must connect directly to PG or a session-mode pooler;
// transaction-mode pooling is incompatible with session advisory locks.
// DSN values and driver errors are never included in returned errors.
func AcquireLease(ctx context.Context, dsn, job string, options LeaseOptions) (*Lease, error) {
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(job) == "" || strings.TrimSpace(dsn) == "" {
		return nil, errors.New("lease requires job and database DSN")
	}
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, errors.New("open lease database failed")
	}
	network := &leaseDialer{}
	connector.Dialer(network)
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	connectCtx, cancel := context.WithTimeout(ctx, options.CheckTimeout)
	defer cancel()
	stopConnect := context.AfterFunc(connectCtx, network.close)
	conn, err := db.Conn(connectCtx)
	stopConnect()
	if err != nil {
		network.close()
		_ = db.Close()
		return nil, errors.New("connect lease session failed")
	}
	return acquireLease(ctx, &sqlLeaseSession{db: db, conn: conn, network: network}, job, options)
}
func leaseKey(job string) int64 {
	sum := sha256.Sum256([]byte("newapi2acg/job-lease/v1\x00" + job))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
func acquireLease(parent context.Context, session leaseSession, job string, options LeaseOptions) (*Lease, error) {
	options, err := options.normalized()
	if err != nil {
		_ = session.close()
		return nil, err
	}
	if strings.TrimSpace(job) == "" {
		_ = session.close()
		return nil, errors.New("lease requires job")
	}
	ctx, cancel := context.WithTimeout(parent, options.CheckTimeout)
	held, err := session.tryLock(ctx, leaseKey(job))
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	if err != nil || parent.Err() != nil {
		_ = session.close()
		return nil, errors.New("acquire job lease failed")
	}
	if !held {
		_ = session.close()
		return nil, ErrLeaseHeld
	}
	leaseCtx, leaseCancel := context.WithCancelCause(parent)
	l := &Lease{ctx: leaseCtx, cancel: leaseCancel, done: make(chan struct{})}
	go l.monitor(session, options)
	return l, nil
}
func (l *Lease) monitor(session leaseSession, options LeaseOptions) {
	defer close(l.done)
	defer func() {
		if err := session.close(); err != nil {
			l.closeErr = errors.New("close lease session failed")
		}
	}()
	ticker := time.NewTicker(options.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.ctx, options.CheckTimeout)
			result := make(chan error, 1)
			go func() { result <- session.ping(ctx) }()
			var err error
			select {
			case err = <-result:
				if err == nil {
					err = ctx.Err()
				}
			case <-ctx.Done():
				err = ctx.Err()
			}
			cancel()
			if err != nil {
				l.cancel(ErrLeaseLost)
				return
			}
		}
	}
}

// leaseDialer retains every socket belonging to this session (including driver
// cancellation sockets), so a hung query can be interrupted without waiting on
// database/sql's per-connection mutex. It rejects all dials after shutdown.
type leaseDialer struct {
	mu      sync.Mutex
	sockets []net.Conn
	closed  bool
}

func (d *leaseDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}
func (d *leaseDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, network, address)
}
func (d *leaseDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, ErrLeaseClosed
	}
	c, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		_ = c.Close()
		return nil, ErrLeaseClosed
	}
	d.sockets = append(d.sockets, c)
	return c, nil
}
func (d *leaseDialer) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	for _, c := range d.sockets {
		_ = c.Close()
	}
}
