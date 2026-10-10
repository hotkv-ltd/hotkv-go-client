// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Pool manages a pool of connections to a HotKV server.
type Pool struct {
	opts *Options

	mu       sync.Mutex
	conns    []*Conn // idle connections
	allConns map[*Conn]struct{}
	size     int32 // total connections (idle + in-use)
	closed   int32

	sem chan struct{} // limits max connections

	// Stats.
	stats PoolStats
}

// PoolStats contains pool usage statistics.
type PoolStats struct {
	Hits       uint32
	Misses     uint32
	Timeouts   uint32
	TotalConns uint32
	IdleConns  uint32
	StaleConns uint32
}

// newPool creates a new connection pool.
func newPool(opts *Options) *Pool {
	p := &Pool{
		opts:     opts,
		conns:    make([]*Conn, 0, opts.PoolSize),
		allConns: make(map[*Conn]struct{}),
		sem:      make(chan struct{}, opts.PoolSize),
	}

	// Pre-fill semaphore.
	for i := 0; i < opts.PoolSize; i++ {
		p.sem <- struct{}{}
	}

	// Start idle connection reaper.
	if opts.IdleCheckFrequency > 0 {
		go p.reaper(opts.IdleCheckFrequency)
	}

	return p
}

// Get returns an idle connection or creates a new one.
func (p *Pool) Get(ctx context.Context) (*Conn, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrClosed
	}

	// Acquire semaphore to respect pool size.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.sem:
		// Acquired a slot.
	default:
		// Pool exhausted, wait with timeout.
		timer := time.NewTimer(p.opts.PoolTimeout)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
			atomic.AddUint32(&p.stats.Timeouts, 1)
			return nil, ErrPoolTimeout
		case <-p.sem:
			timer.Stop()
		}
	}

	// Try to get an idle connection.
	p.mu.Lock()
	for len(p.conns) > 0 {
		n := len(p.conns) - 1
		cn := p.conns[n]
		p.conns = p.conns[:n]

		if cn.IsClosed() || cn.isExpired(p.opts.MaxConnAge, p.opts.IdleTimeout) {
			p.removeConnLocked(cn)
			cn.Close()
			atomic.AddUint32(&p.stats.StaleConns, 1)
			continue
		}

		p.mu.Unlock()
		atomic.AddUint32(&p.stats.Hits, 1)
		return cn, nil
	}
	p.mu.Unlock()

	// No idle connection available; dial a new one.
	atomic.AddUint32(&p.stats.Misses, 1)
	cn, err := dial(ctx, p.opts)
	if err != nil {
		// Return the semaphore slot.
		p.sem <- struct{}{}
		return nil, err
	}

	p.mu.Lock()
	if atomic.LoadInt32(&p.closed) == 1 {
		// The pool was closed while this connection was being dialled.
		p.mu.Unlock()
		cn.Close()
		p.sem <- struct{}{}
		return nil, ErrClosed
	}
	p.allConns[cn] = struct{}{}
	atomic.AddInt32(&p.size, 1)
	atomic.AddUint32(&p.stats.TotalConns, 1)
	p.mu.Unlock()

	return cn, nil
}

// Put returns a connection to the pool.
func (p *Pool) Put(cn *Conn) {
	if cn.IsClosed() {
		p.Remove(cn)
		return
	}

	cn.usedAt = time.Now()

	p.mu.Lock()
	if atomic.LoadInt32(&p.closed) == 1 {
		p.mu.Unlock()
		cn.Close()
		p.sem <- struct{}{}
		return
	}
	p.conns = append(p.conns, cn)
	p.mu.Unlock()

	// Release semaphore slot.
	p.sem <- struct{}{}
}

// Remove discards a connection from the pool (e.g., on error).
func (p *Pool) Remove(cn *Conn) {
	p.mu.Lock()
	p.removeConnLocked(cn)
	p.mu.Unlock()

	cn.Close()

	// Release the semaphore slot.
	p.sem <- struct{}{}
}

func (p *Pool) removeConnLocked(cn *Conn) {
	if _, ok := p.allConns[cn]; ok {
		delete(p.allConns, cn)
		atomic.AddInt32(&p.size, -1)
	}
}

// Len returns the number of idle connections.
func (p *Pool) Len() int {
	p.mu.Lock()
	n := len(p.conns)
	p.mu.Unlock()
	return n
}

// Size returns the total number of connections (idle + in-use).
func (p *Pool) Size() int {
	return int(atomic.LoadInt32(&p.size))
}

// Stats returns a snapshot of pool statistics.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Hits:       atomic.LoadUint32(&p.stats.Hits),
		Misses:     atomic.LoadUint32(&p.stats.Misses),
		Timeouts:   atomic.LoadUint32(&p.stats.Timeouts),
		TotalConns: atomic.LoadUint32(&p.stats.TotalConns),
		IdleConns:  uint32(p.Len()),
		StaleConns: atomic.LoadUint32(&p.stats.StaleConns),
	}
}

// Close closes all connections in the pool.
func (p *Pool) Close() error {
	if !atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		return ErrClosed
	}

	p.mu.Lock()
	for cn := range p.allConns {
		cn.Close()
	}
	p.allConns = make(map[*Conn]struct{})
	p.conns = nil
	p.mu.Unlock()

	return nil
}

// reaper periodically removes idle connections.
func (p *Pool) reaper(frequency time.Duration) {
	ticker := time.NewTicker(frequency)
	defer ticker.Stop()

	for range ticker.C {
		if atomic.LoadInt32(&p.closed) == 1 {
			return
		}
		p.reapStaleConns()
	}
}

func (p *Pool) reapStaleConns() {
	var stale []*Conn

	p.mu.Lock()
	alive := make([]*Conn, 0, len(p.conns))
	for _, cn := range p.conns {
		if cn.isExpired(p.opts.MaxConnAge, p.opts.IdleTimeout) {
			p.removeConnLocked(cn)
			stale = append(stale, cn)
		} else {
			alive = append(alive, cn)
		}
	}
	p.conns = alive
	p.mu.Unlock()

	for _, cn := range stale {
		cn.Close()
		atomic.AddUint32(&p.stats.StaleConns, 1)
		// No semaphore release here: a connection sitting in the idle list
		// already gave its slot back in Put. Releasing again would let the
		// pool grow past PoolSize (or block this goroutine once the
		// semaphore is full).
	}
}
