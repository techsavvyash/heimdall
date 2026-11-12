package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"time"

	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// NewConnector snapshots configuration without connecting. Each connection, including
// TLS and PostgreSQL startup, is bounded by five seconds or earlier cancellation.
func NewConnector(dsn string) (driver.Connector, error) {
	cfg, err := pq.NewConfig(dsn)
	if err != nil {
		return nil, contract.ErrInvalidArgument
	}
	if cfg.ConnectTimeout <= 0 || cfg.ConnectTimeout > 5*time.Second {
		cfg.ConnectTimeout = 5 * time.Second
	}
	return &boundedConnector{cfg: cfg.Clone()}, nil
}

type boundedConnector struct{ cfg pq.Config }

func (c *boundedConnector) Driver() driver.Driver { return &pq.Driver{} }
func (c *boundedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	connector, e := pq.NewConnectorConfig(c.cfg.Clone())
	if e != nil {
		return nil, contract.ErrInvalidArgument
	}
	dialer := &startupDialer{ctx: ctx}
	connector.Dialer(dialer)
	conn, e := connector.Connect(ctx)
	// Stop and join cancellation callbacks before handing a live socket to the pool.
	for _, socket := range dialer.sockets {
		if !socket.stop() {
			<-socket.done
		}
	}
	if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
		for _, socket := range dialer.sockets {
			_ = socket.conn.Close()
		}
		return nil, cancelled
	}
	if e != nil {
		for _, socket := range dialer.sockets {
			_ = socket.conn.Close()
		}
		// The socket deadline can fire before the context timer is scheduled.
		var timeout net.Error
		if errors.As(e, &timeout) && timeout.Timeout() {
			return nil, contract.ErrDeadlineExceeded
		}
		return nil, contract.ErrUnavailable
	}
	return conn, nil
}

type startupSocket struct {
	conn net.Conn
	stop func() bool
	done chan struct{}
}
type startupDialer struct {
	ctx     context.Context
	sockets []startupSocket
}

func (d *startupDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(d.ctx, network, address)
}
func (d *startupDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	return d.DialContext(ctx, network, address)
}
func (d *startupDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, e := (&net.Dialer{}).DialContext(ctx, network, address)
	if e != nil {
		return nil, e
	}
	done := make(chan struct{})
	// lib/pq does not watch the operation context while reading startup responses.
	stop := context.AfterFunc(d.ctx, func() { _ = conn.Close(); close(done) })
	d.sockets = append(d.sockets, startupSocket{conn, stop, done})
	return conn, nil
}
