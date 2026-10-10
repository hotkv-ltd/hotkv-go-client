// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"errors"
	"testing"
	"time"
)

func TestConnReleaseAndCloseFreePoolSlots(t *testing.T) {
	// two connections are available: take both, give one back with Release
	// and discard the other with Close; the pool must then hand out two more
	srv := newFakeCluster(t, 1)
	c, err := NewClient(&Options{Addr: srv.addr(0), PoolSize: 2, PoolTimeout: 300 * time.Millisecond, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := c.Conn(ctxT())
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Conn(ctxT())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Conn(ctxT()); !errors.Is(err, ErrPoolTimeout) {
		t.Fatalf("third Conn from a pool of 2 = %v, want ErrPoolTimeout", err)
	}
	a.Release()
	b.Close()
	x, err := c.Conn(ctxT())
	if err != nil {
		t.Fatalf("Conn after Release: %v", err)
	}
	y, err := c.Conn(ctxT())
	if err != nil {
		t.Fatalf("Conn after Close: %v", err)
	}
	x.Close()
	y.Release()
	a.Release() // a second Release is a no-op
	if v, err := c.Ping(ctxT()).Result(); err != nil || v != "PONG" {
		t.Fatalf("PING after all that = %q, %v", v, err)
	}
}
