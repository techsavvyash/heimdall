package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestConnectorStalledStartup(t *testing.T) {
	for _, mode := range []string{"disable", "require"} {
		for _, policy := range []string{"deadline", "cancel", "cap", "short-cap"} {
			if (policy == "cap" || policy == "short-cap") && mode == "require" {
				continue
			}
			t.Run(mode+"-"+policy, func(t *testing.T) {
				listener, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				defer listener.Close()
				accepted := make(chan net.Conn, 1)
				closed := make(chan struct{})
				go func() {
					conn, e := listener.Accept()
					if e != nil {
						return
					}
					if mode == "require" {
						// Accept PostgreSQL's SSLRequest, then stall inside TLS.
						if _, e = io.ReadFull(conn, make([]byte, 8)); e != nil {
							_ = conn.Close()
							close(closed)
							return
						}
						if _, e = conn.Write([]byte("S")); e != nil {
							_ = conn.Close()
							close(closed)
							return
						}
						if _, e = io.ReadFull(conn, make([]byte, 1)); e != nil {
							_ = conn.Close()
							close(closed)
							return
						}
					}
					accepted <- conn
					_, _ = io.Copy(io.Discard, conn)
					_ = conn.Close()
					close(closed)
				}()
				dsn := "postgres://test:secret@" + listener.Addr().String() + "/test?sslmode=" + mode
				if policy == "short-cap" {
					dsn += "&connect_timeout=1"
				}
				connector, e := NewConnector(dsn)
				if e != nil {
					t.Fatal(e)
				}
				ctx, cancel := context.WithCancel(context.Background())
				if policy == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
				}
				defer cancel()
				result := make(chan error, 1)
				go func() {
					conn, e := connector.Connect(ctx)
					if conn != nil {
						_ = conn.Close()
					}
					result <- e
				}()
				select {
				case conn := <-accepted:
					defer conn.Close()
				case <-time.After(2 * time.Second):
					t.Fatal("no TCP connection")
				}
				if policy == "cancel" {
					cancel()
				}
				want := contract.ErrDeadlineExceeded
				if policy == "cancel" {
					want = contract.ErrCancelled
				}
				select {
				case e := <-result:
					if !errors.Is(e, want) {
						t.Fatal(e)
					}
				case <-time.After(7 * time.Second):
					t.Fatal("startup ignored cancellation")
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("startup socket leaked")
				}
			})
		}
	}
}
func TestConnectorConfiguration(t *testing.T) {
	if _, e := NewConnector("postgres://test:%zz@localhost/db"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	for _, dsn := range []string{"postgres://test@localhost/db?connect_timeout=0", "postgres://test@localhost/db?connect_timeout=60"} {
		c, e := NewConnector(dsn)
		if e != nil {
			t.Fatal(e)
		}
		if c.(*boundedConnector).cfg.ConnectTimeout != 5*time.Second {
			t.Fatal("connection cap")
		}
	}
	c, e := NewConnector("postgres://test@localhost/db?connect_timeout=1")
	if e != nil {
		t.Fatal(e)
	}
	if c.(*boundedConnector).cfg.ConnectTimeout != time.Second {
		t.Fatal("shorter timeout changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.Connect(ctx); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e = c.Connect(nil); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestConnectorPostgresReuse(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_READY_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	var before, after int
	if e = db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&before); e != nil {
		t.Fatal(e)
	}
	cancel()
	if e = db.QueryRowContext(context.Background(), "SELECT pg_backend_pid()").Scan(&after); e != nil || before != after {
		t.Fatal(before, after, e)
	}
	if e = CheckCapacityReadiness(context.Background(), db); e != nil {
		t.Fatal(e)
	}
}
