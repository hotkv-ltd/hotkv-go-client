// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Tests for reply decoding edge cases.

func TestBoolCmdSetNXReplies(t *testing.T) {
	cases := []struct {
		reply string
		want  bool
	}{
		{"+OK\r\n", true},  // SET ... NX/XX that wrote
		{"$-1\r\n", false}, // SET ... NX/XX that did not
		{"_\r\n", false},   // the same in RESP3
		{":1\r\n", true},   // SETNX-style integer replies
		{":0\r\n", false},
		{"#t\r\n", true},
		{"#f\r\n", false},
	}
	for _, c := range cases {
		cl, _ := captureArgs(t, c.reply)
		got, err := cl.SetNX(context.Background(), "k", "v", 0).Result()
		if err != nil || got != c.want {
			t.Errorf("SetNX with reply %q = %v, %v; want %v", c.reply, got, err, c.want)
		}
	}
}

func TestNullIntegerAndFloatReplyAreNil(t *testing.T) {
	cl, _ := captureArgs(t, "$-1\r\n")
	if n, err := cl.ZRank(context.Background(), "z", "missing").Result(); err != Nil {
		t.Errorf("ZRank of a missing member = %d, %v; want Nil (not rank 0)", n, err)
	}
	cl, _ = captureArgs(t, "$-1\r\n")
	if f, err := cl.ZScore(context.Background(), "z", "missing").Result(); err != Nil {
		t.Errorf("ZScore of a missing member = %v, %v; want Nil", f, err)
	}
	cl, _ = captureArgs(t, ":0\r\n")
	if n, err := cl.ZRank(context.Background(), "z", "first").Result(); err != nil || n != 0 {
		t.Errorf("ZRank of the first member = %d, %v; want 0", n, err)
	}
}

func TestHotKeysTopParsesNestedPairs(t *testing.T) {
	nested := "*2\r\n*2\r\n$1\r\na\r\n:7\r\n*2\r\n$1\r\nb\r\n:3\r\n"
	cl, _ := captureArgs(t, nested)
	got, err := cl.HotKeysTop(context.Background(), 2).Result()
	want := []HotKeyEntry{{Key: "a", Count: 7}, {Key: "b", Count: 3}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("HotKeysTop (nested) = %+v, %v; want %+v", got, err, want)
	}
	flat := "*4\r\n$1\r\na\r\n:7\r\n$1\r\nb\r\n:3\r\n"
	cl, _ = captureArgs(t, flat)
	got, err = cl.HotKeysTop(context.Background(), 2).Result()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("HotKeysTop (flat) = %+v, %v; want %+v", got, err, want)
	}
}

func TestMapStringStringStringifiesIntegers(t *testing.T) {
	// PUBSUB NUMSUB: channel, count, channel, count with integer counts
	cl, _ := captureArgs(t, "*4\r\n$2\r\nc1\r\n:3\r\n$2\r\nc2\r\n:0\r\n")
	got, err := cl.PubSubNumSub(context.Background(), "c1", "c2").Result()
	want := map[string]string{"c1": "3", "c2": "0"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("PubSubNumSub = %v, %v; want %v", got, err, want)
	}
}

func TestPubSubPongShapes(t *testing.T) {
	ps := &PubSub{}
	for name, v := range map[string]Value{
		"RESP2 array":  {Typ: RespArray, Elems: []Value{{Typ: RespBulkString, Str: "pong"}, {Typ: RespBulkString, Str: "x"}}},
		"RESP3 simple": {Typ: RespSimpleString, Str: "x"},
		"RESP3 bulk":   {Typ: RespBulkString, Str: "x"},
	} {
		m, err := ps.parseMessage(v)
		p, ok := m.(*Pong)
		if err != nil || !ok || p.Payload != "x" {
			t.Errorf("%s: parseMessage = %#v, %v", name, m, err)
		}
	}
}

func TestPoolReaperDoesNotInflateTheSemaphore(t *testing.T) {
	srv := newFakeCluster(t, 1)
	c, err := NewClient(&Options{
		Addr: srv.addr(0), PoolSize: 2, MaxRetries: -1, IdleTimeout: 20 * time.Millisecond,
		IdleCheckFrequency: -1, PoolTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		a, _ := c.Conn(ctx)
		b, _ := c.Conn(ctx)
		a.Release()
		b.Release()
	}
	time.Sleep(60 * time.Millisecond) // both idle connections are now stale
	done := make(chan struct{})
	go func() { c.pool.reapStaleConns(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reapStaleConns blocked: it released a pool slot it did not hold")
	}
	// still exactly two slots
	x, err1 := c.Conn(ctx)
	y, err2 := c.Conn(ctx)
	_, err3 := c.Conn(ctx)
	if err1 != nil || err2 != nil || err3 != ErrPoolTimeout {
		t.Errorf("after reaping: %v %v %v, want nil nil ErrPoolTimeout", err1, err2, err3)
	}
	x.Close()
	y.Close()
}
