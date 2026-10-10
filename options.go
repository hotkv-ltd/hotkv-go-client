// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"crypto/tls"
	"time"
)

// Options configures a HotKV client.
type Options struct {
	// Addr is the host:port address of the HotKV server.
	// Default: "localhost:6379".
	Addr string

	// Password for AUTH. Empty string disables authentication.
	Password string

	// Username for ACL-based authentication (HotKV 1.0+). Leave empty for
	// legacy single-password AUTH.
	Username string

	// DB selects the database number (SELECT command on connect).
	// Default: 0.
	DB int

	// PoolSize is the maximum number of connections in the pool.
	// Default: 10.
	PoolSize int

	// MinIdleConns is the minimum number of idle connections kept open.
	// Default: 0.
	MinIdleConns int

	// DialTimeout is the timeout for establishing new connections.
	// Default: 5s.
	DialTimeout time.Duration

	// ReadTimeout is the timeout for reading a single response.
	// Default: 3s. Use -1 for no timeout.
	ReadTimeout time.Duration

	// WriteTimeout is the timeout for writing a single command.
	// Default: same as ReadTimeout.
	WriteTimeout time.Duration

	// PoolTimeout is how long to wait for a free connection from the pool.
	// Default: ReadTimeout + 1s.
	PoolTimeout time.Duration

	// MaxRetries is the maximum number of retries on network errors.
	// Default: 3 (0 selects the default). Use -1 to disable retries.
	MaxRetries int

	// MinRetryBackoff is the minimum backoff between retries.
	// Default: 8ms. Use -1 to disable backoff.
	MinRetryBackoff time.Duration

	// MaxRetryBackoff is the maximum backoff between retries.
	// Default: 512ms. Use -1 to disable backoff.
	MaxRetryBackoff time.Duration

	// TLSConfig configures TLS for the connection. Nil disables TLS.
	TLSConfig *tls.Config

	// Protocol selects the RESP protocol version (2 or 3).
	// Default: 3 (RESP3). The client sends HELLO on connect.
	Protocol int

	// OnConnect is an optional callback called after a connection is established
	// and authenticated. It can be used for custom initialization.
	OnConnect func(*Conn) error

	// MaxConnAge is the maximum lifetime of a connection.
	// Expired connections are closed lazily on return to the pool.
	// Default: 0 (no age limit).
	MaxConnAge time.Duration

	// IdleTimeout is the amount of time a connection may be idle before being
	// closed. Default: 5m.
	IdleTimeout time.Duration

	// IdleCheckFrequency is how often idle connections are reaped.
	// Default: 1m. Use -1 to disable the reaper.
	IdleCheckFrequency time.Duration
}

// retries is the effective number of retries (never negative).
func (o *Options) retries() int {
	if o.MaxRetries < 0 {
		return 0
	}
	return o.MaxRetries
}

func (o *Options) init() {
	if o.Addr == "" {
		o.Addr = "localhost:6379"
	}
	if o.PoolSize <= 0 {
		o.PoolSize = 10
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = 3 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = o.ReadTimeout
	}
	if o.PoolTimeout == 0 {
		o.PoolTimeout = o.ReadTimeout + time.Second
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 3
	}
	// A negative MaxRetries (retries disabled) stays negative so that
	// init is idempotent; use retries() to read the effective count.
	if o.MinRetryBackoff == 0 {
		o.MinRetryBackoff = 8 * time.Millisecond
	}
	if o.MaxRetryBackoff == 0 {
		o.MaxRetryBackoff = 512 * time.Millisecond
	}
	if o.Protocol == 0 {
		o.Protocol = 3
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = 5 * time.Minute
	}
	if o.IdleCheckFrequency == 0 {
		o.IdleCheckFrequency = time.Minute
	}
}
