// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestCluster(t *testing.T, fc *fakeCluster, mod func(*ClusterOptions)) *ClusterClient {
	t.Helper()
	o := &ClusterOptions{
		Addrs: []string{fc.addr(0)},
		Options: Options{
			DialTimeout:     time.Second,
			ReadTimeout:     2 * time.Second,
			MinRetryBackoff: -1,
			MaxRetryBackoff: -1,
		},
	}
	if mod != nil {
		mod(o)
	}
	cc, err := NewClusterClient(o)
	if err != nil {
		t.Fatalf("NewClusterClient: %v", err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func ctxT() context.Context { return context.Background() }

func TestClusterOptionsValidation(t *testing.T) {
	if _, err := NewClusterClient(nil); err == nil {
		t.Error("nil options accepted")
	}
	if _, err := NewClusterClient(&ClusterOptions{}); err == nil {
		t.Error("no seeds accepted")
	}
	cc, err := NewClusterClient(&ClusterOptions{Options: Options{Addr: "h:1"}})
	if err != nil || cc.opts.Addrs[0] != "h:1" {
		t.Errorf("Options.Addr must be the fallback seed: %v %v", cc, err)
	}
	o, err := ParseClusterURL("hotkv://u:p@a:1/0", "hotkv://b:2")
	if err != nil || len(o.Addrs) != 2 || o.Password != "p" || o.Username != "u" || o.Addrs[1] != "b:2" {
		t.Errorf("ParseClusterURL = %+v, %v", o, err)
	}
}

func TestClusterRoutesByHashSlot(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)

	for i := 0; i < 60; i++ {
		k := fmt.Sprintf("key:%d", i)
		if err := cc.Set(ctxT(), k, "v"+k, 0).Err(); err != nil {
			t.Fatalf("SET %s: %v", k, err)
		}
	}
	for i := 0; i < 60; i++ {
		k := fmt.Sprintf("key:%d", i)
		v, err := cc.Get(ctxT(), k).Result()
		if err != nil || v != "v"+k {
			t.Fatalf("GET %s = %q, %v", k, v, err)
		}
		owner := fc.owner[slotOfKey(k)]
		if _, ok := fc.nodes[owner].data[k]; !ok {
			t.Fatalf("key %s not stored on its owner node %d", k, owner)
		}
	}
	// No redirect was ever needed: a correct slot map sends each command
	// straight to the owner, and CLUSTER SLOTS was fetched exactly once.
	slots := 0
	for _, n := range fc.nodes {
		slots += n.count("CLUSTER SLOTS")
	}
	if slots != 1 {
		t.Errorf("CLUSTER SLOTS fetched %d times, want 1", slots)
	}
	// All three nodes got traffic.
	for i, n := range fc.nodes {
		if len(n.data) == 0 {
			t.Errorf("node %d holds no keys", i)
		}
	}
}

func TestClusterHashTagColocates(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	if err := cc.MSet(ctxT(), "{user1}.a", "1", "{user1}.b", "2").Err(); err != nil {
		t.Fatalf("MSET with a shared hash tag: %v", err)
	}
	owner := fc.owner[slotOfKey("user1")]
	for _, k := range []string{"{user1}.a", "{user1}.b"} {
		if _, ok := fc.nodes[owner].data[k]; !ok {
			t.Errorf("%s not on the owner of slot(user1)", k)
		}
	}
}

func TestClusterMovedRedirectUpdatesRoute(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	k := fc.keyOwnedBy(0, "mv")
	if err := cc.Set(ctxT(), k, "old", 0).Err(); err != nil {
		t.Fatal(err)
	}
	// Reshard behind the client's back: slot of k now belongs to node 2.
	fc.nodes[2].data[k] = fc.nodes[0].data[k]
	delete(fc.nodes[0].data, k)
	fc.setOwner(slotOfKey(k), 2)

	v, err := cc.Get(ctxT(), k).Result()
	if err != nil || v != "old" {
		t.Fatalf("GET after MOVED = %q, %v", v, err)
	}
	before := fc.nodes[0].count("GET")
	for i := 0; i < 5; i++ {
		if v, err := cc.Get(ctxT(), k).Result(); err != nil || v != "old" {
			t.Fatalf("GET #%d = %q, %v", i, v, err)
		}
	}
	if got := fc.nodes[0].count("GET"); got != before {
		t.Errorf("node 0 saw %d more GETs after the MOVED hint, want 0", got-before)
	}
	// The slot map is refreshed in the background.
	deadline := time.Now().Add(2 * time.Second)
	for cc.SlotAddr(uint16(slotOfKey(k))) != fc.addr(2) {
		if time.Now().After(deadline) {
			t.Fatalf("slot map not refreshed: slot owner %s, want %s", cc.SlotAddr(uint16(slotOfKey(k))), fc.addr(2))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClusterAskRedirectSendsAsking(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	k := fc.keyOwnedBy(0, "ask")
	slot := slotOfKey(k)
	if err := cc.Set(ctxT(), "warm", "x", 0).Err(); err != nil { // loads the topology
		t.Fatal(err)
	}
	// Slot is migrating node 0 -> node 1; the key is already on node 1.
	fc.mu.Lock()
	fc.migrating[slot] = 1
	fc.importing[slot] = 0
	fc.mu.Unlock()
	fc.nodes[1].data[k] = "moved-value"

	v, err := cc.Get(ctxT(), k).Result()
	if err != nil || v != "moved-value" {
		t.Fatalf("GET through ASK = %q, %v", v, err)
	}
	log := fc.nodes[1].logged()
	var askIdx, getIdx = -1, -1
	for i, l := range log {
		if l == "ASKING" && askIdx < 0 {
			askIdx = i
		}
		if l == "GET "+k {
			getIdx = i
		}
	}
	if askIdx < 0 || getIdx != askIdx+1 {
		t.Errorf("target log %v: want ASKING immediately before GET %s", log, k)
	}
	// ASK is one-shot: the slot map still says node 0 for the next call,
	// which is asked again and redirected again.
	before := fc.nodes[0].count("GET")
	if _, err := cc.Get(ctxT(), k).Result(); err != nil {
		t.Fatal(err)
	}
	if fc.nodes[0].count("GET") != before+1 {
		t.Error("an ASK redirect must not permanently re-route the slot")
	}
	// A SET (key absent on node 0 is also ASKed in the fake): same path.
	if err := cc.Set(ctxT(), k, "again", 0).Err(); err != nil {
		t.Fatalf("SET through ASK: %v", err)
	}
}

func TestClusterMaxRedirects(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, func(o *ClusterOptions) { o.MaxRedirects = 3 })
	if err := cc.Set(ctxT(), "warm", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	fc.alwaysMov = true
	fc.mu.Unlock()
	err := cc.Get(ctxT(), "loop").Err()
	if !IsMovedError(err) {
		t.Fatalf("err = %v, want the final MOVED after MaxRedirects", err)
	}
	total := 0
	for _, n := range fc.nodes {
		total += n.count("GET loop")
	}
	if total != 4 { // the first try plus 3 redirects
		t.Errorf("GET sent %d times, want 4 (1 + MaxRedirects)", total)
	}
}

func TestClusterTryAgainRetries(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	if err := cc.Set(ctxT(), "warm", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	fc.tryAgain = 2
	fc.mu.Unlock()
	if err := cc.Set(ctxT(), "k", "v", 0).Err(); err != nil {
		t.Fatalf("SET after two TRYAGAIN: %v", err)
	}
}

func TestClusterNodeFailureRefreshesAndRetries(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, func(o *ClusterOptions) { o.Addrs = fc.addrs() })
	k := fc.keyOwnedBy(1, "fail")
	if err := cc.Set(ctxT(), k, "v1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	// Node 1 dies; its slots fail over to node 2, which has the data.
	fc.nodes[2].data[k] = "v1"
	fc.nodes[1].kill()
	for s := 0; s < clusterSlots; s++ {
		if fc.owner[s] == 1 {
			fc.setOwner(s, 2)
		}
	}
	v, err := cc.Get(ctxT(), k).Result()
	if err != nil || v != "v1" {
		t.Fatalf("GET after node failure = %q, %v", v, err)
	}
	if got := cc.SlotAddr(uint16(slotOfKey(k))); got != fc.addr(2) {
		t.Errorf("slot owner after reload = %s, want %s", got, fc.addr(2))
	}
}

func TestClusterBlockingCommandNotRetried(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	if err := cc.Set(ctxT(), "warm", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	k := fc.keyOwnedBy(0, "blk")
	fc.nodes[0].kill()
	err := cc.BLPop(ctxT(), time.Second, k).Err()
	if err == nil {
		t.Fatal("BLPOP on a dead node must fail, not retry silently")
	}
}

func TestClusterPipelineGroupsByNode(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	p := cc.Pipeline()
	var sets []*Cmd
	for i := 0; i < 30; i++ {
		sets = append(sets, p.Do(ctxT(), "SET", fmt.Sprintf("pk:%d", i), i))
	}
	var gets []*Cmd
	for i := 0; i < 30; i++ {
		gets = append(gets, p.Do(ctxT(), "GET", fmt.Sprintf("pk:%d", i)))
	}
	if _, err := p.Exec(ctxT()); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	for i, c := range sets {
		if s, err := c.Text(); err != nil || s != "OK" {
			t.Fatalf("SET %d = %q, %v", i, s, err)
		}
	}
	for i, c := range gets {
		if s, err := c.Text(); err != nil || s != fmt.Sprint(i) {
			t.Fatalf("GET %d = %q, %v (result order must match queue order)", i, s, err)
		}
	}
	// 60 commands, 3 nodes: every node got its share in one connection's
	// worth of traffic, not a command per round trip through random nodes.
	for i, n := range fc.nodes {
		if n.count("SET") == 0 || n.count("GET") == 0 {
			t.Errorf("node %d got no pipeline traffic", i)
		}
	}
}

func TestClusterPipelineHandlesMoved(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	if err := cc.Set(ctxT(), "warm", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	k := fc.keyOwnedBy(0, "pm")
	fc.nodes[1].data[k] = "val"
	fc.setOwner(slotOfKey(k), 1)
	p := cc.Pipeline()
	get := p.Do(ctxT(), "GET", k)
	if _, err := p.Exec(ctxT()); err != nil {
		t.Fatal(err)
	}
	if s, err := get.Text(); err != nil || s != "val" {
		t.Fatalf("GET via pipeline MOVED = %q, %v", s, err)
	}
}

func TestClusterTxPipelineSingleSlot(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	p := cc.TxPipeline()
	a := p.Do(ctxT(), "INCR", "{tx}a")
	b := p.Do(ctxT(), "INCR", "{tx}a")
	if _, err := p.Exec(ctxT()); err != nil {
		t.Fatalf("tx Exec: %v", err)
	}
	if x, _ := a.Int64(); x != 1 {
		t.Errorf("first INCR = %d", x)
	}
	if x, _ := b.Int64(); x != 2 {
		t.Errorf("second INCR = %d", x)
	}

	// Keys in different slots cannot share a MULTI/EXEC.
	p2 := cc.TxPipeline()
	p2.Do(ctxT(), "INCR", fc.keyOwnedBy(0, "x"))
	p2.Do(ctxT(), "INCR", fc.keyOwnedBy(1, "y"))
	_, err := p2.Exec(ctxT())
	var e *Error
	if !errors.As(err, &e) || e.Prefix() != "CROSSSLOT" {
		t.Fatalf("cross-slot tx err = %v, want CROSSSLOT", err)
	}
}

func TestClusterReadOnlyUsesReplicas(t *testing.T) {
	fc := newFakeCluster(t, 3)
	// Node 2 is a replica of node 0 only (owns no slots).
	fc.setOwnerRange(0, clusterSlots-1, 0)
	fc.setOwnerRange(clusterSlots/2, clusterSlots-1, 1)
	fc.mu.Lock()
	fc.replicas[0] = []int{2}
	fc.mu.Unlock()
	k := fc.keyOwnedBy(0, "ro")
	fc.nodes[0].data[k] = "v"
	fc.nodes[2].data[k] = "v"

	cc := newTestCluster(t, fc, func(o *ClusterOptions) { o.ReadOnly = true })
	for i := 0; i < 30; i++ {
		if v, err := cc.Get(ctxT(), k).Result(); err != nil || v != "v" {
			t.Fatalf("GET = %q, %v", v, err)
		}
	}
	if fc.nodes[2].count("GET") == 0 {
		t.Error("ReadOnly=true never used the replica")
	}
	if fc.nodes[2].count("READONLY") == 0 {
		t.Error("replica connections must send READONLY")
	}
	// Writes always go to the primary.
	if err := cc.Set(ctxT(), k, "w", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if fc.nodes[2].count("SET") != 0 {
		t.Error("a write reached the replica")
	}
}

func TestClusterForEachPrimaryAndKeyless(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	if err := cc.Ping(ctxT()).Err(); err != nil {
		t.Fatalf("PING: %v", err)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	err := cc.ForEachPrimary(ctxT(), func(ctx context.Context, n *Client) error {
		mu.Lock()
		seen[n.Options().Addr] = true
		mu.Unlock()
		return n.Ping(ctx).Err()
	})
	if err != nil || len(seen) != 3 {
		t.Fatalf("ForEachPrimary visited %v, err %v", seen, err)
	}
	prim, err := cc.Primaries(ctxT())
	if err != nil || len(prim) != 3 {
		t.Fatalf("Primaries = %v, %v", prim, err)
	}
}

func TestClusterWatchPinsConnection(t *testing.T) {
	fc := newFakeCluster(t, 3)
	cc := newTestCluster(t, fc, nil)
	err := cc.Watch(ctxT(), func(tx *Tx) error {
		if err := tx.Do(ctxT(), "SET", "{w}k", "1").Err(); err != nil {
			return err
		}
		p := tx.Pipeline()
		p.Do(ctxT(), "INCR", "{w}k")
		_, err := p.Exec(ctxT())
		return err
	}, "{w}k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	owner := fc.owner[slotOfKey("w")]
	if fc.nodes[owner].data["{w}k"] != "2" {
		t.Errorf("value = %q, want 2 (INCR ran on the watched node)", fc.nodes[owner].data["{w}k"])
	}
	log := strings.Join(fc.nodes[owner].logged(), "|")
	if !strings.Contains(log, "WATCH {w}k") || !strings.Contains(log, "MULTI") || !strings.Contains(log, "EXEC") {
		t.Errorf("owner log lacks WATCH/MULTI/EXEC: %s", log)
	}
	if err := cc.Watch(ctxT(), func(tx *Tx) error { return nil }, "{a}", "{b}"); err == nil {
		t.Error("cross-slot Watch accepted")
	}
}

func TestClusterNoTopologyError(t *testing.T) {
	cc, _ := NewClusterClient(&ClusterOptions{
		Addrs:   []string{"127.0.0.1:1"},
		Options: Options{DialTimeout: 200 * time.Millisecond, MinRetryBackoff: -1, MaxRetryBackoff: -1},
	})
	defer cc.Close()
	err := cc.Get(ctxT(), "k").Err()
	if err == nil || !strings.Contains(err.Error(), "topology") {
		t.Fatalf("err = %v, want a topology error", err)
	}
}

func TestClusterClosed(t *testing.T) {
	fc := newFakeCluster(t, 1)
	cc := newTestCluster(t, fc, nil)
	cc.Close()
	if err := cc.Get(ctxT(), "k").Err(); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// --- key extraction from the COMMAND table ---

func TestKeyExtractionFromCommandTable(t *testing.T) {
	fc := newFakeCluster(t, 1)
	cc := newTestCluster(t, fc, nil)
	if err := cc.Ping(ctxT()).Err(); err != nil {
		t.Fatal(err)
	}
	tbl := cc.commandTable(ctxT())
	if tbl == nil {
		t.Fatal("no command table")
	}
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"GET", "a"}, []string{"a"}},
		{[]string{"MGET", "a", "b", "c"}, []string{"a", "b", "c"}},
		{[]string{"MSET", "a", "1", "b", "2"}, []string{"a", "b"}},
		{[]string{"DEL", "a", "b"}, []string{"a", "b"}},
		{[]string{"EVAL", "return 1", "2", "k1", "k2", "arg"}, []string{"k1", "k2"}},
		{[]string{"EVAL", "return 1", "0", "arg"}, nil},
		{[]string{"XREAD", "COUNT", "5", "STREAMS", "s1", "s2", "0", "0"}, []string{"s1", "s2"}},
		{[]string{"BITOP", "AND", "dest", "s1", "s2"}, []string{"dest", "s1", "s2"}},
		{[]string{"OBJECT", "ENCODING", "k"}, []string{"k"}},
		{[]string{"OBJECT", "HELP"}, nil},
		{[]string{"PING"}, nil},
		{[]string{"DBSIZE"}, nil},
	}
	for _, c := range cases {
		ci := tbl.lookup(c.args)
		if ci == nil {
			t.Errorf("%v: no table entry", c.args)
			continue
		}
		var got []string
		for _, i := range ci.keyIndexes(c.args) {
			got = append(got, c.args[i])
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("keys(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestKeyExtractionLegacyTriple(t *testing.T) {
	ci := &cmdInfo{first: 1, last: -1, step: 2}
	idx := ci.keyIndexes([]string{"MSET", "a", "1", "b", "2"})
	if !reflect.DeepEqual(idx, []int{1, 3}) {
		t.Errorf("legacy step-2 indexes = %v", idx)
	}
	if got := (&cmdInfo{}).keyIndexes([]string{"PING"}); got != nil {
		t.Errorf("keyless = %v", got)
	}
}

func TestParseRedirect(t *testing.T) {
	s, a, ok := parseRedirect("MOVED 3999 127.0.0.1:6381")
	if !ok || s != 3999 || a != "127.0.0.1:6381" {
		t.Errorf("parseRedirect = %d %q %v", s, a, ok)
	}
	if _, _, ok := parseRedirect("MOVED x y"); ok {
		t.Error("bad slot accepted")
	}
	if _, _, ok := parseRedirect("MOVED 99999 h:1"); ok {
		t.Error("out of range slot accepted")
	}
	if got := hostFallback(":6381", "10.0.0.5:6379"); got != "10.0.0.5:6381" {
		t.Errorf("hostFallback = %q", got)
	}
}
