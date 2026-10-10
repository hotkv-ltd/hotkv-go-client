// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"time"
)

// Client is the main HotKV client. It manages a connection pool and provides
// typed methods for the command families listed in the README.
// Any other command is available through Do.
type Client struct {
	opts *Options
	pool *Pool

	// fixed, when set, pins every command to one connection (used by Watch,
	// whose WATCH state lives on a single connection).
	fixed *Conn

	// cluster, when set, routes every command through the cluster client
	// instead of a single-node pool (see ClusterClient).
	cluster *ClusterClient
}

// newClient creates a Client with the given options. Does not connect eagerly.
func newClient(opts *Options) *Client {
	opts.init()
	return &Client{
		opts: opts,
		pool: newPool(opts),
	}
}

// Options returns the client's configuration (read-only snapshot).
func (c *Client) Options() *Options {
	return c.opts
}

// PoolStats returns pool usage statistics.
func (c *Client) PoolStats() PoolStats {
	if c.pool == nil {
		return PoolStats{}
	}
	return c.pool.Stats()
}

// Close closes the client, releasing all connections.
func (c *Client) Close() error {
	if c.pool == nil {
		return nil
	}
	return c.pool.Close()
}

// Conn takes a dedicated connection out of the pool, for commands that
// depend on connection state (MULTI, WATCH, CLIENT TRACKING, ...). Give it
// back with cn.Release() when it is in a clean state, or discard it with
// cn.Close(); either one frees its pool slot, and until then it counts
// against PoolSize.
func (c *Client) Conn(ctx context.Context) (*Conn, error) {
	if c.pool == nil {
		return nil, errNoSingleConn
	}
	cn, err := c.pool.Get(ctx)
	if err != nil {
		return nil, err
	}
	cn.lentTo.Store(c.pool)
	return cn, nil
}

var errNoSingleConn = errors.New("hotkv: this client has no single connection pool (cluster client or Watch transaction); use Do, a Pipeline, or ForEachPrimary")

// getConn takes the connection a single command will use.
func (c *Client) getConn(ctx context.Context) (*Conn, error) {
	if c.fixed != nil {
		return c.fixed, nil
	}
	if c.pool == nil {
		return nil, errNoSingleConn
	}
	return c.pool.Get(ctx)
}

// putConn gives back a healthy connection.
func (c *Client) putConn(cn *Conn) {
	if c.fixed != nil {
		return
	}
	c.pool.Put(cn)
}

// dropConn discards a connection after an I/O error.
func (c *Client) dropConn(cn *Conn) {
	if c.fixed != nil {
		_ = cn.Close()
		return
	}
	c.pool.Remove(cn)
}

// process executes a command: get conn, send, read, return conn.
func (c *Client) process(ctx context.Context, args ...string) (Value, error) {
	if err := checkArgs(args); err != nil {
		return Value{}, err
	}
	if c.cluster != nil {
		return c.cluster.route(ctx, args)
	}
	return c.processOn(ctx, 0, false, args)
}

// processBlocking executes a command that may legitimately block on the
// server for up to wait (0 = indefinitely, bounded only by ctx): the read
// deadline is stretched to wait plus the usual ReadTimeout, and the command
// is sent exactly once (a timed-out blocking pop must never be retried: the
// server may already have popped).
func (c *Client) processBlocking(ctx context.Context, wait time.Duration, args ...string) (Value, error) {
	if err := checkArgs(args); err != nil {
		return Value{}, err
	}
	if c.cluster != nil {
		return c.cluster.routeBlocking(ctx, wait, args)
	}
	return c.processOn(ctx, wait, true, args)
}

func (c *Client) processOn(ctx context.Context, wait time.Duration, blocking bool, args []string) (Value, error) {
	var lastErr error

	maxAttempts := c.opts.retries()
	if blocking || c.fixed != nil {
		maxAttempts = 0
	}

	for attempt := 0; attempt <= maxAttempts; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, attempt); err != nil {
				return Value{}, err
			}
		}

		cn, err := c.getConn(ctx)
		if err != nil {
			lastErr = err
			if errors.Is(err, errNoSingleConn) || ctx.Err() != nil {
				return Value{}, err
			}
			continue
		}

		var val Value
		if blocking {
			val, err = cn.doBlocking(ctx, wait, args...)
		} else {
			val, err = cn.Do(ctx, args...)
		}
		if err != nil {
			c.dropConn(cn)
			if !blocking && isRetryableError(err) {
				lastErr = err
				continue
			}
			return Value{}, err
		}

		c.putConn(cn)

		// Check for RESP error.
		if val.IsErr() {
			return val, NewError(val.Str)
		}
		return val, nil
	}

	return Value{}, lastErr
}

// Do runs any command, typed or not, and returns its raw reply. Arguments
// may be strings, []byte, integers, floats (written as plain decimals,
// never in exponent form; NaN and Inf are rejected), bools ("1"/"0"),
// fmt.Stringer or encoding.BinaryMarshaler values; any other type is an
// error and nothing is sent.
//
//	n, err := client.Do(ctx, "BF.RESERVE", "bf", "0.01", 1000).Result()
//	v, err := client.Do(ctx, "HGETDEL", "h", "FIELDS", 1, "f1").Slice()
//
// A null reply is not an error on the Cmd itself: Err is nil and the typed
// accessors (Text, Int64, Result, ...) report it as Nil.
func (c *Client) Do(ctx context.Context, args ...interface{}) *Cmd {
	cmd := &Cmd{}
	if len(args) == 0 {
		cmd.err = errors.New("hotkv: Do requires a command name")
		return cmd
	}
	strs, err := convertArgs(args)
	if err != nil {
		cmd.err = err
		return cmd
	}
	c.processCmd(ctx, cmd, strs...)
	return cmd
}

// processCmd is a helper that calls process and populates a Cmd.
func (c *Client) processCmd(ctx context.Context, cmd *Cmd, args ...string) {
	val, err := c.process(ctx, args...)
	cmd.SetVal(val)
	cmd.SetErr(err)
}

func (c *Client) sleep(ctx context.Context, attempt int) error {
	if c.opts.MinRetryBackoff < 0 || c.opts.MaxRetryBackoff < 0 {
		return nil
	}

	d := c.retryBackoff(attempt)
	if d == 0 {
		return nil
	}

	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) retryBackoff(attempt int) time.Duration {
	base := c.opts.MinRetryBackoff
	if base <= 0 {
		return 0
	}

	// Exponential backoff with jitter.
	d := base << uint(attempt-1)
	if d > c.opts.MaxRetryBackoff {
		d = c.opts.MaxRetryBackoff
	}

	// Add jitter: [0.5*d, 1.5*d).
	jitter := time.Duration(rand.Int63n(int64(d)))
	d = d/2 + jitter

	return d
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Network errors (also when wrapped) and a connection the server
	// closed while it sat idle in the pool are retryable.
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "use of closed network connection")
}

// Pipeline returns a new pipeline for batching commands.
func (c *Client) Pipeline() *Pipeline {
	return newPipeline(c)
}

// TxPipeline returns a pipeline wrapped in MULTI/EXEC.
func (c *Client) TxPipeline() *Pipeline {
	return newTxPipeline(c)
}

// Subscribe creates a new Pub/Sub subscription.
func (c *Client) Subscribe(ctx context.Context, channels ...string) *PubSub {
	ps := newPubSub(c)
	if len(channels) > 0 {
		_ = ps.Subscribe(ctx, channels...)
	}
	return ps
}

// PSubscribe creates a new pattern-based Pub/Sub subscription.
func (c *Client) PSubscribe(ctx context.Context, patterns ...string) *PubSub {
	ps := newPubSub(c)
	if len(patterns) > 0 {
		_ = ps.PSubscribe(ctx, patterns...)
	}
	return ps
}
