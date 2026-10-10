// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Message represents a message received on a Pub/Sub channel.
type Message struct {
	Channel string
	Pattern string
	Payload string
}

// Subscription represents a subscribe/unsubscribe confirmation.
type Subscription struct {
	Kind    string // "subscribe", "unsubscribe", "psubscribe", "punsubscribe"
	Channel string
	Count   int
}

// Pong represents a PONG response to a PING on the Pub/Sub connection.
type Pong struct {
	Payload string
}

// PubSub manages a Pub/Sub connection to a HotKV server.
// Messages are delivered via the Channel() or ReceiveMessage() API.
type PubSub struct {
	client *Client
	mu     sync.Mutex
	cn     *Conn
	closed bool

	channels map[string]struct{}
	patterns map[string]struct{}

	msgCh  chan *Message
	allCh  chan interface{} // Message, Subscription, or Pong
	chOnce sync.Once
	done   chan struct{} // closed by Close; the receive loop's only exit signal
}

func newPubSub(c *Client) *PubSub {
	return &PubSub{
		client:   c,
		channels: make(map[string]struct{}),
		patterns: make(map[string]struct{}),
		done:     make(chan struct{}),
	}
}

// conn returns or creates the dedicated Pub/Sub connection.
func (ps *PubSub) conn(ctx context.Context) (*Conn, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.closed {
		return nil, ErrClosed
	}
	if ps.cn != nil {
		return ps.cn, nil
	}

	cn, err := dial(ctx, ps.client.opts)
	if err != nil {
		return nil, err
	}
	ps.cn = cn

	// Re-subscribe to any channels/patterns (for reconnection).
	if len(ps.channels) > 0 {
		chans := mapKeys(ps.channels)
		if err := ps.subscribe(ctx, cn, "SUBSCRIBE", chans...); err != nil {
			cn.Close()
			ps.cn = nil
			return nil, err
		}
	}
	if len(ps.patterns) > 0 {
		pats := mapKeys(ps.patterns)
		if err := ps.subscribe(ctx, cn, "PSUBSCRIBE", pats...); err != nil {
			cn.Close()
			ps.cn = nil
			return nil, err
		}
	}

	return cn, nil
}

// Subscribe subscribes to the given channels.
func (ps *PubSub) Subscribe(ctx context.Context, channels ...string) error {
	ps.mu.Lock()
	for _, ch := range channels {
		ps.channels[ch] = struct{}{}
	}
	cn := ps.cn
	ps.mu.Unlock()

	if cn == nil {
		var err error
		cn, err = ps.conn(ctx)
		if err != nil {
			return err
		}
		return nil // conn() already subscribes
	}

	return ps.subscribe(ctx, cn, "SUBSCRIBE", channels...)
}

// PSubscribe subscribes to channels matching the given patterns.
func (ps *PubSub) PSubscribe(ctx context.Context, patterns ...string) error {
	ps.mu.Lock()
	for _, p := range patterns {
		ps.patterns[p] = struct{}{}
	}
	cn := ps.cn
	ps.mu.Unlock()

	if cn == nil {
		var err error
		cn, err = ps.conn(ctx)
		if err != nil {
			return err
		}
		return nil
	}

	return ps.subscribe(ctx, cn, "PSUBSCRIBE", patterns...)
}

// Unsubscribe unsubscribes from the given channels, or all if none specified.
func (ps *PubSub) Unsubscribe(ctx context.Context, channels ...string) error {
	ps.mu.Lock()
	if len(channels) == 0 {
		ps.channels = make(map[string]struct{})
	} else {
		for _, ch := range channels {
			delete(ps.channels, ch)
		}
	}
	cn := ps.cn
	ps.mu.Unlock()

	if cn == nil {
		return nil
	}
	return ps.subscribe(ctx, cn, "UNSUBSCRIBE", channels...)
}

// PUnsubscribe unsubscribes from the given patterns, or all if none specified.
func (ps *PubSub) PUnsubscribe(ctx context.Context, patterns ...string) error {
	ps.mu.Lock()
	if len(patterns) == 0 {
		ps.patterns = make(map[string]struct{})
	} else {
		for _, p := range patterns {
			delete(ps.patterns, p)
		}
	}
	cn := ps.cn
	ps.mu.Unlock()

	if cn == nil {
		return nil
	}
	return ps.subscribe(ctx, cn, "PUNSUBSCRIBE", patterns...)
}

func (ps *PubSub) subscribe(ctx context.Context, cn *Conn, cmd string, args ...string) error {
	cn.mu.Lock()
	defer cn.mu.Unlock()

	cmdArgs := make([]string, 0, 1+len(args))
	cmdArgs = append(cmdArgs, cmd)
	cmdArgs = append(cmdArgs, args...)

	if err := cn.wr.WriteCommand(cmdArgs...); err != nil {
		return err
	}
	return cn.wr.Flush()
}

// Ping sends a PING on the Pub/Sub connection.
func (ps *PubSub) Ping(ctx context.Context, payload ...string) error {
	cn, err := ps.conn(ctx)
	if err != nil {
		return err
	}

	cn.mu.Lock()
	defer cn.mu.Unlock()

	args := []string{"PING"}
	if len(payload) > 0 {
		args = append(args, payload[0])
	}
	if err := cn.wr.WriteCommand(args...); err != nil {
		return err
	}
	return cn.wr.Flush()
}

// Close closes the Pub/Sub connection.
func (ps *PubSub) Close() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.closed {
		return nil
	}
	ps.closed = true
	// The receive loop is the only sender on msgCh, so it (not Close)
	// closes the channel once it sees done: closing here could race with a
	// send and panic.
	close(ps.done)

	if ps.cn != nil {
		return ps.cn.Close()
	}
	return nil
}

// ReceiveTimeout reads one message from the Pub/Sub connection.
// It returns a *Message, *Subscription, or *Pong.
func (ps *PubSub) ReceiveTimeout(ctx context.Context) (interface{}, error) {
	cn, err := ps.conn(ctx)
	if err != nil {
		return nil, err
	}

	val, err := cn.ReadValue(ctx)
	if err != nil {
		ps.reconnect(ctx)
		return nil, err
	}

	return ps.parseMessage(val)
}

// ReceiveMessage reads the next Message, skipping subscription confirmations.
func (ps *PubSub) ReceiveMessage(ctx context.Context) (*Message, error) {
	for {
		msg, err := ps.ReceiveTimeout(ctx)
		if err != nil {
			return nil, err
		}
		if m, ok := msg.(*Message); ok {
			return m, nil
		}
	}
}

// Channel returns a Go channel that receives messages. The channel is created
// on the first call and delivers only *Message values (not subscriptions).
// The channel is closed when PubSub.Close() is called.
func (ps *PubSub) Channel() <-chan *Message {
	ps.chOnce.Do(func() {
		ps.msgCh = make(chan *Message, 100)
		go ps.receiveLoop()
	})
	return ps.msgCh
}

func (ps *PubSub) receiveLoop() {
	defer close(ps.msgCh)
	backoff := 50 * time.Millisecond
	for {
		select {
		case <-ps.done:
			return
		default:
		}

		msg, err := ps.ReceiveTimeout(context.Background())
		if err != nil {
			// The connection broke (ReceiveTimeout dropped it and the next
			// call re-dials and re-subscribes). Pause so that a server that
			// is down does not turn this into a busy loop.
			select {
			case <-ps.done:
				return
			case <-time.After(backoff):
			}
			if backoff < time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 50 * time.Millisecond
		if m, ok := msg.(*Message); ok {
			select {
			case ps.msgCh <- m:
			case <-ps.done:
				return
			}
		}
	}
}

func (ps *PubSub) parseMessage(val Value) (interface{}, error) {
	// RESP3 answers PING on a subscribed connection like any other
	// connection: a simple (or bulk) string, not a pub/sub array.
	if val.Typ == RespSimpleString || val.Typ == RespBulkString {
		return &Pong{Payload: val.Str}, nil
	}
	if val.Typ == RespArray && len(val.Elems) == 2 && val.Elems[0].Str == "pong" {
		return &Pong{Payload: val.Elems[1].Str}, nil
	}
	if val.Typ != RespArray && val.Typ != RespPush {
		return nil, fmt.Errorf("hotkv: pubsub: unexpected type %d", val.Typ)
	}

	if len(val.Elems) < 3 {
		return nil, fmt.Errorf("hotkv: pubsub: expected at least 3 elements, got %d", len(val.Elems))
	}

	kind := val.Elems[0].Str

	switch kind {
	case "subscribe", "unsubscribe", "psubscribe", "punsubscribe":
		count := int(val.Elems[2].Int)
		return &Subscription{
			Kind:    kind,
			Channel: val.Elems[1].Str,
			Count:   count,
		}, nil

	case "message":
		return &Message{
			Channel: val.Elems[1].Str,
			Payload: val.Elems[2].Str,
		}, nil

	case "pmessage":
		if len(val.Elems) < 4 {
			return nil, fmt.Errorf("hotkv: pubsub: pmessage needs 4 elements")
		}
		return &Message{
			Pattern: val.Elems[1].Str,
			Channel: val.Elems[2].Str,
			Payload: val.Elems[3].Str,
		}, nil

	case "pong":
		payload := ""
		if len(val.Elems) >= 2 {
			payload = val.Elems[1].Str
		}
		return &Pong{Payload: payload}, nil

	default:
		return nil, fmt.Errorf("hotkv: pubsub: unknown message kind %q", kind)
	}
}

func (ps *PubSub) reconnect(ctx context.Context) {
	ps.mu.Lock()
	if ps.cn != nil {
		ps.cn.Close()
		ps.cn = nil
	}
	ps.mu.Unlock()
}

func mapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
