// Package mysql implements source.Source and source.Streamer for MySQL with
// github.com/go-mysql-org/go-mysql: the client package for snapshot reads and
// the replication package for binlog subscription. Snapshot, binlog and
// precheck all use the one instance named by the DSN (design-core §5.4).
package mysql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
	gomysqldriver "github.com/go-mysql-org/go-mysql/driver"
	gomysql "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
)

type Options struct {
	DSN             string // user:password@host:port/database (go-mysql form)
	ServerID        uint32
	PositionMode    string // auto | gtid | filepos
	Datasets        []config.Dataset
	LockWaitTimeout time.Duration
	ConnectTimeout  time.Duration
	Logger          *slog.Logger
}

// conn is the subset of *client.Conn the source uses (fakeable in tests).
type conn interface {
	Execute(command string, args ...any) (*gomysql.Result, error)
	Close() error
}

type Source struct {
	opts     Options
	host     string
	port     uint16
	user     string
	password string
	db       string
	datasets map[string]config.Dataset // by dataset name
	byTable  map[string]config.Dataset // by table name
	connect  func(ctx context.Context) (conn, error)
	log      *slog.Logger
}

var (
	_ source.Source   = (*Source)(nil)
	_ source.Streamer = (*Source)(nil)
)

// New validates options without connecting. Errors never include the DSN.
func New(o Options) (*Source, error) {
	c, err := gomysqldriver.ParseDSN(o.DSN)
	if err != nil || c.Addr == "" || c.DB == "" {
		return nil, errors.New("invalid MySQL DSN; expected user:password@host:port/database")
	}
	host, portText, err := net.SplitHostPort(c.Addr)
	if err != nil {
		host, portText = c.Addr, "3306"
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, errors.New("invalid MySQL DSN port")
	}
	if o.ServerID == 0 {
		return nil, errors.New("binlog server_id is required")
	}
	if o.LockWaitTimeout <= 0 {
		o.LockWaitTimeout = 10 * time.Second
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 10 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Source{opts: o, host: host, port: uint16(port), user: c.User, password: c.Password, db: c.DB,
		datasets: map[string]config.Dataset{}, byTable: map[string]config.Dataset{}, log: o.Logger}
	for _, d := range o.Datasets {
		s.datasets[d.Name] = d
		s.byTable[d.Table] = d
	}
	addr := net.JoinHostPort(host, portText)
	s.connect = func(ctx context.Context) (conn, error) {
		cc, err := client.ConnectWithContext(ctx, addr, s.user, s.password, s.db, o.ConnectTimeout)
		if err != nil {
			return nil, fmt.Errorf("connect to source MySQL failed: %s", describe(err))
		}
		return cc, nil
	}
	return s, nil
}

// Schema returns the source database name.
func (s *Source) Schema() string { return s.db }

// describe summarizes a driver error without echoing arbitrary server text.
func describe(err error) string {
	var me *gomysql.MyError
	if errors.As(err, &me) {
		return fmt.Sprintf("mysql error %d", me.Code)
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return "network error"
	}
	return "connection error"
}

func errorCode(err error) uint16 {
	var me *gomysql.MyError
	if errors.As(err, &me) {
		return me.Code
	}
	return 0
}

// execute runs one statement, closing the connection if ctx ends first
// (go-mysql client calls are not context-aware).
func execute(ctx context.Context, c conn, q string, args ...any) (*gomysql.Result, error) {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	r, err := c.Execute(q, args...)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return r, err
}

// readPosition reads the current binlog position. MySQL 8.2+ uses SHOW
// BINARY LOG STATUS; older servers SHOW MASTER STATUS.
func (s *Source) readPosition(ctx context.Context, c conn) (event.SourcePosition, error) {
	r, err := execute(ctx, c, "SHOW BINARY LOG STATUS")
	if errorCode(err) == 1064 {
		r, err = execute(ctx, c, "SHOW MASTER STATUS")
	}
	if err != nil {
		return event.SourcePosition{}, fmt.Errorf("read binlog position failed: %s", describe(err))
	}
	if r == nil || r.Resultset == nil || r.RowNumber() == 0 {
		return event.SourcePosition{}, errors.New("read binlog position failed: binary logging is disabled")
	}
	file, err := r.GetStringByName(0, "File")
	if err != nil {
		return event.SourcePosition{}, errors.New("read binlog position failed: no File column")
	}
	pos, err := r.GetUintByName(0, "Position")
	if err != nil {
		return event.SourcePosition{}, errors.New("read binlog position failed: no Position column")
	}
	gtid, _ := r.GetStringByName(0, "Executed_Gtid_Set")
	return s.makePosition(file, pos, gtid)
}

func (s *Source) makePosition(file string, pos uint64, gtid string) (event.SourcePosition, error) {
	gtid = compactGTID(gtid)
	p := event.SourcePosition{BinlogFile: file, BinlogPos: pos}
	switch s.opts.PositionMode {
	case "gtid":
		if gtid == "" {
			return event.SourcePosition{}, errors.New("position_mode gtid requires gtid_mode=ON on the source")
		}
		p.Mode, p.GTIDSet = event.ModeGTID, gtid
	case "filepos":
		p.Mode = event.ModeFilePos
	default: // auto
		if gtid != "" {
			p.Mode, p.GTIDSet = event.ModeGTID, gtid
		} else {
			p.Mode = event.ModeFilePos
		}
	}
	return p, nil
}

func compactGTID(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r != ' ' && r != '\n' && r != '\r' && r != '\t' {
			out = append(out, r)
		}
	}
	return string(out)
}

// CurrentPosition reads the source's latest binlog position.
func (s *Source) CurrentPosition(ctx context.Context) (event.SourcePosition, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return event.SourcePosition{}, err
	}
	defer func() { _ = c.Close() }()
	return s.readPosition(ctx, c)
}
