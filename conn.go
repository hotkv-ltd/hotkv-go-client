// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Conn is a single connection to a HotKV server.
// It wraps a TCP connection with a buffered RESP reader and writer.
type Conn struct {
	mu        sync.Mutex
	netConn   net.Conn
	rd        *RespReader
	wr        *RespWriter
	opts      *Options
	usedAt    time.Time
	createdAt time.Time
	closed    bool
	pooled    bool
	protocol  int // negotiated RESP protocol version

	// lentTo is the pool a connection obtained from Client.Conn was taken
	// from; Release gives it back and Close discards it, both freeing the
	// pool slot.
	lentTo atomic.Pointer[Pool]
}

// dial creates a new TCP connection to the server.
func dial(ctx context.Context, opts *Options) (*Conn, error) {
	var d net.Dialer
	d.Timeout = opts.DialTimeout

	var netConn net.Conn
	var err error

	if opts.TLSConfig != nil {
		tlsDialer := &tls.Dialer{
			NetDialer: &d,
			Config:    opts.TLSConfig,
		}
		netConn, err = tlsDialer.DialContext(ctx, "tcp", opts.Addr)
	} else {
		netConn, err = d.DialContext(ctx, "tcp", opts.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("hotkv: dial %s: %w", opts.Addr, err)
	}

	c := &Conn{
		netConn:   netConn,
		rd:        NewRespReader(netConn),
		wr:        NewRespWriter(netConn),
		opts:      opts,
		createdAt: time.Now(),
		usedAt:    time.Now(),
		protocol:  2, // default until HELLO negotiation
	}

	if err := c.init(ctx); err != nil {
		netConn.Close()
		return nil, err
	}

	return c, nil
}

// init performs the post-connect handshake: HELLO, AUTH, SELECT.
func (c *Conn) init(ctx context.Context) error {
	// HELLO negotiation (RESP3 default).
	if c.opts.Protocol >= 3 {
		if err := c.hello(ctx); err != nil {
			// Fall back to RESP2 if HELLO is not supported.
			c.protocol = 2
		}
	}

	// AUTH if password is set.
	if c.opts.Password != "" {
		if err := c.auth(ctx); err != nil {
			return err
		}
	}

	// SELECT database.
	if c.opts.DB != 0 {
		if err := c.selectDB(ctx); err != nil {
			return err
		}
	}

	// OnConnect callback.
	if c.opts.OnConnect != nil {
		if err := c.opts.OnConnect(c); err != nil {
			return fmt.Errorf("hotkv: OnConnect: %w", err)
		}
	}

	return nil
}

func (c *Conn) hello(ctx context.Context) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}

	args := []string{"HELLO", strconv.Itoa(c.opts.Protocol)}
	if c.opts.Password != "" {
		if c.opts.Username != "" {
			args = append(args, "AUTH", c.opts.Username, c.opts.Password)
		} else {
			args = append(args, "AUTH", "default", c.opts.Password)
		}
	}

	if err := c.wr.WriteCommand(args...); err != nil {
		return err
	}
	if err := c.wr.Flush(); err != nil {
		return err
	}

	if err := c.setReadDeadline(ctx); err != nil {
		return err
	}

	val, err := c.rd.ReadValue()
	if err != nil {
		return err
	}
	if val.IsErr() {
		return NewError(val.Str)
	}
	c.protocol = c.opts.Protocol
	return nil
}

func (c *Conn) auth(ctx context.Context) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}

	var args []string
	if c.opts.Username != "" {
		args = []string{"AUTH", c.opts.Username, c.opts.Password}
	} else {
		args = []string{"AUTH", c.opts.Password}
	}

	if err := c.wr.WriteCommand(args...); err != nil {
		return err
	}
	if err := c.wr.Flush(); err != nil {
		return err
	}

	if err := c.setReadDeadline(ctx); err != nil {
		return err
	}
	val, err := c.rd.ReadValue()
	if err != nil {
		return fmt.Errorf("hotkv: AUTH: %w", err)
	}
	if val.IsErr() {
		return NewError(val.Str)
	}
	return nil
}

func (c *Conn) selectDB(ctx context.Context) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}

	if err := c.wr.WriteCommand("SELECT", strconv.Itoa(c.opts.DB)); err != nil {
		return err
	}
	if err := c.wr.Flush(); err != nil {
		return err
	}

	if err := c.setReadDeadline(ctx); err != nil {
		return err
	}
	val, err := c.rd.ReadValue()
	if err != nil {
		return fmt.Errorf("hotkv: SELECT: %w", err)
	}
	if val.IsErr() {
		return NewError(val.Str)
	}
	return nil
}

// Do sends a command and reads the response.
func (c *Conn) Do(ctx context.Context, args ...string) (Value, error) {
	return c.do(ctx, 0, false, args)
}

// doBlocking is Do for a command the server may hold for up to wait (0 =
// no server-side limit): the read deadline is wait plus the configured
// ReadTimeout (or just ctx's deadline when wait is 0).
func (c *Conn) doBlocking(ctx context.Context, wait time.Duration, args ...string) (Value, error) {
	return c.do(ctx, wait, true, args)
}

func (c *Conn) do(ctx context.Context, wait time.Duration, blocking bool, args []string) (Value, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.usedAt = time.Now()

	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	// A cancelled context must interrupt a read that is waiting on the
	// server: expire the deadline so the read returns at once.
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() {
			_ = c.netConn.SetDeadline(time.Now())
		})
		defer func() {
			// stop reports false when the callback already started; it may
			// then expire the deadline after this call returns, so the
			// connection is not safe to reuse.
			if !stop() {
				c.closed = true
				_ = c.netConn.Close()
			}
		}()
	}

	if err := c.setWriteDeadline(ctx); err != nil {
		return Value{}, err
	}
	if err := c.wr.WriteCommand(args...); err != nil {
		return Value{}, ctxErr(ctx, err)
	}
	if err := c.wr.Flush(); err != nil {
		return Value{}, ctxErr(ctx, err)
	}

	var err error
	if blocking {
		err = c.setBlockingReadDeadline(ctx, wait)
	} else {
		err = c.setReadDeadline(ctx)
	}
	if err != nil {
		return Value{}, err
	}
	val, err := c.rd.ReadValue()
	if err != nil {
		return Value{}, ctxErr(ctx, err)
	}
	return val, nil
}

// ctxErr prefers the context's error when the context is what interrupted
// the I/O.
func ctxErr(ctx context.Context, err error) error {
	if ce := ctx.Err(); ce != nil {
		return ce
	}
	return err
}

func (c *Conn) setBlockingReadDeadline(ctx context.Context, wait time.Duration) error {
	var dl time.Time
	if wait > 0 {
		grace := c.opts.ReadTimeout
		if grace <= 0 {
			grace = 3 * time.Second
		}
		dl = time.Now().Add(wait + grace)
	}
	if cd, ok := ctx.Deadline(); ok && (dl.IsZero() || cd.Before(dl)) {
		dl = cd
	}
	return c.netConn.SetReadDeadline(dl)
}

// WriteCommand writes a command without reading the response (for pipelining).
func (c *Conn) WriteCommand(ctx context.Context, args ...string) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}
	return c.wr.WriteCommand(args...)
}

// FlushAndRead flushes buffered commands and reads one response.
func (c *Conn) FlushAndRead(ctx context.Context) (Value, error) {
	if err := c.wr.Flush(); err != nil {
		return Value{}, err
	}
	if err := c.setReadDeadline(ctx); err != nil {
		return Value{}, err
	}
	return c.rd.ReadValue()
}

// Flush flushes the write buffer.
func (c *Conn) Flush() error {
	return c.wr.Flush()
}

// ReadValue reads one RESP value.
func (c *Conn) ReadValue(ctx context.Context) (Value, error) {
	if err := c.setReadDeadline(ctx); err != nil {
		return Value{}, err
	}
	return c.rd.ReadValue()
}

// Release returns a connection obtained from Client.Conn to the client's
// pool for reuse. Only call it on a connection in a clean state (no open
// MULTI, WATCH or subscription); otherwise use Close. It is a no-op for a
// connection that was not lent by Client.Conn.
func (c *Conn) Release() {
	if p := c.lentTo.Swap(nil); p != nil {
		p.Put(c)
	}
}

// Close closes the underlying network connection. For a connection lent by
// Client.Conn it also frees its slot in the client's pool.
func (c *Conn) Close() error {
	if p := c.lentTo.Swap(nil); p != nil {
		p.Remove(c) // closes the connection and releases the slot
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.netConn.Close()
}

// IsClosed reports whether the connection is closed.
func (c *Conn) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// NetConn returns the underlying net.Conn. Use with caution.
func (c *Conn) NetConn() net.Conn {
	return c.netConn
}

func (c *Conn) setWriteDeadline(ctx context.Context) error {
	if c.opts.WriteTimeout > 0 {
		return c.netConn.SetWriteDeadline(time.Now().Add(c.opts.WriteTimeout))
	}
	if dl, ok := ctx.Deadline(); ok {
		return c.netConn.SetWriteDeadline(dl)
	}
	return c.netConn.SetWriteDeadline(time.Time{})
}

func (c *Conn) setReadDeadline(ctx context.Context) error {
	if c.opts.ReadTimeout > 0 {
		return c.netConn.SetReadDeadline(time.Now().Add(c.opts.ReadTimeout))
	}
	if dl, ok := ctx.Deadline(); ok {
		return c.netConn.SetReadDeadline(dl)
	}
	return c.netConn.SetReadDeadline(time.Time{})
}

func (c *Conn) isExpired(maxAge, idleTimeout time.Duration) bool {
	now := time.Now()
	if maxAge > 0 && now.Sub(c.createdAt) > maxAge {
		return true
	}
	if idleTimeout > 0 && now.Sub(c.usedAt) > idleTimeout {
		return true
	}
	return false
}

// doMulti writes several commands, flushes once, and reads one reply per
// command (RESP pipelining on this connection).
func (c *Conn) doMulti(ctx context.Context, cmds [][]string) ([]Value, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.usedAt = time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.setWriteDeadline(ctx); err != nil {
		return nil, err
	}
	for _, args := range cmds {
		if err := c.wr.WriteCommand(args...); err != nil {
			return nil, err
		}
	}
	if err := c.wr.Flush(); err != nil {
		return nil, err
	}
	out := make([]Value, len(cmds))
	for i := range cmds {
		if err := c.setReadDeadline(ctx); err != nil {
			return nil, err
		}
		v, err := c.rd.ReadValue()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
