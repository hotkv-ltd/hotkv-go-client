// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A small in-process fake of a HotKV cluster for ClusterClient tests: N
// listeners that answer CLUSTER SLOTS and COMMAND, own slot ranges, serve a
// handful of key commands, and reply MOVED / ASK / TRYAGAIN exactly like a
// real node would. Tests mutate the topology (move a slot, kill a node,
// start a migration) and assert what the client did.

func arr(items ...string) string {
	return "*" + strconv.Itoa(len(items)) + "\r\n" + strings.Join(items, "")
}
func bulk(s string) string { return respBulk(s) }
func intr(n int) string    { return respInt(int64(n)) }

func cmdEntry(name string, arity int, flags []string, first, last, step int, specs []string, subs []string) string {
	fl := make([]string, len(flags))
	for i, f := range flags {
		fl[i] = "+" + f + "\r\n"
	}
	return arr(bulk(name), intr(arity), arr(fl...), intr(first), intr(last), intr(step),
		arr(), arr(), arr(specs...), arr(subs...))
}

func specRange(index, lastkey, keystep, limit int) string {
	return arr(bulk("flags"), arr(), bulk("begin_search"),
		arr(bulk("type"), bulk("index"), bulk("spec"), arr(bulk("index"), intr(index))),
		bulk("find_keys"),
		arr(bulk("type"), bulk("range"), bulk("spec"), arr(bulk("lastkey"), intr(lastkey), bulk("keystep"), intr(keystep), bulk("limit"), intr(limit))))
}

func specKeynum(index, keynumidx, firstkey, keystep int) string {
	return arr(bulk("flags"), arr(), bulk("begin_search"),
		arr(bulk("type"), bulk("index"), bulk("spec"), arr(bulk("index"), intr(index))),
		bulk("find_keys"),
		arr(bulk("type"), bulk("keynum"), bulk("spec"), arr(bulk("keynumidx"), intr(keynumidx), bulk("firstkey"), intr(firstkey), bulk("keystep"), intr(keystep))))
}

func specKeyword(kw string, startfrom, lastkey, keystep, limit int) string {
	return arr(bulk("flags"), arr(), bulk("begin_search"),
		arr(bulk("type"), bulk("keyword"), bulk("spec"), arr(bulk("keyword"), bulk(kw), bulk("startfrom"), intr(startfrom))),
		bulk("find_keys"),
		arr(bulk("type"), bulk("range"), bulk("spec"), arr(bulk("lastkey"), intr(lastkey), bulk("keystep"), intr(keystep), bulk("limit"), intr(limit))))
}

// fakeCommandTable is a COMMAND reply with the same shapes the real server
// uses for each kind of key specification.
func fakeCommandTable() string {
	e := []string{
		cmdEntry("get", 2, []string{"readonly", "fast"}, 1, 1, 1, []string{specRange(1, 0, 1, 0)}, nil),
		cmdEntry("set", -3, []string{"write"}, 1, 1, 1, []string{specRange(1, 0, 1, 0)}, nil),
		cmdEntry("incr", 2, []string{"write"}, 1, 1, 1, []string{specRange(1, 0, 1, 0)}, nil),
		cmdEntry("del", -2, []string{"write"}, 1, -1, 1, []string{specRange(1, -1, 1, 0)}, nil),
		cmdEntry("mget", -2, []string{"readonly"}, 1, -1, 1, []string{specRange(1, -1, 1, 0)}, nil),
		cmdEntry("mset", -3, []string{"write"}, 1, -1, 2, []string{specRange(1, -1, 2, 0)}, nil),
		cmdEntry("ping", -1, nil, 0, 0, 0, nil, nil),
		cmdEntry("dbsize", 1, []string{"readonly"}, 0, 0, 0, nil, nil),
		cmdEntry("eval", -3, []string{"movablekeys"}, 0, 0, 0, []string{specKeynum(2, 0, 1, 1)}, nil),
		cmdEntry("xread", -4, []string{"readonly", "movablekeys"}, 0, 0, 0, []string{specKeyword("STREAMS", 1, -1, 1, 2)}, nil),
		cmdEntry("bitop", -4, []string{"write"}, 2, -1, 1, []string{specRange(2, 0, 1, 0), specRange(3, -1, 1, 0)}, nil),
		cmdEntry("object", -2, nil, 0, 0, 0, nil, []string{
			cmdEntry("object|encoding", 3, []string{"readonly"}, 2, 2, 1, []string{specRange(2, 0, 1, 0)}, nil),
			cmdEntry("object|help", 2, nil, 0, 0, 0, nil, nil),
		}),
	}
	return arr(e...)
}

type fcNode struct {
	fc  *fakeCluster
	idx int
	ln  net.Listener

	mu    sync.Mutex
	data  map[string]string
	log   []string
	conns map[net.Conn]struct{}
}

type fakeCluster struct {
	t     *testing.T
	mu    sync.Mutex
	nodes []*fcNode
	owner [clusterSlots]int

	migrating map[int]int   // slot -> target node (on the owner)
	importing map[int]int   // slot -> node it comes from (on the target)
	replicas  map[int][]int // primary idx -> replica idx

	tryAgain  int  // reply TRYAGAIN to this many keyed commands
	alwaysMov bool // every keyed command is answered MOVED to the next node
}

func newFakeCluster(t *testing.T, n int) *fakeCluster {
	t.Helper()
	fc := &fakeCluster{
		t:         t,
		migrating: map[int]int{},
		importing: map[int]int{},
		replicas:  map[int][]int{},
	}
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		nd := &fcNode{fc: fc, idx: i, ln: ln, data: map[string]string{}, conns: map[net.Conn]struct{}{}}
		fc.nodes = append(fc.nodes, nd)
		go nd.accept()
		t.Cleanup(nd.kill)
	}
	per := clusterSlots / n
	for s := 0; s < clusterSlots; s++ {
		o := s / per
		if o >= n {
			o = n - 1
		}
		fc.owner[s] = o
	}
	return fc
}

func (fc *fakeCluster) addr(i int) string { return fc.nodes[i].ln.Addr().String() }

func (fc *fakeCluster) addrs() []string {
	out := make([]string, len(fc.nodes))
	for i := range fc.nodes {
		out[i] = fc.addr(i)
	}
	return out
}

func (fc *fakeCluster) setOwner(slot, node int) {
	fc.mu.Lock()
	fc.owner[slot] = node
	fc.mu.Unlock()
}

func (fc *fakeCluster) setOwnerRange(from, to, node int) {
	fc.mu.Lock()
	for s := from; s <= to; s++ {
		fc.owner[s] = node
	}
	fc.mu.Unlock()
}

// slotOf returns the slot a test key lives in.
func slotOfKey(k string) int { return int(KeySlot(k)) }

// keyOwnedBy finds a key with the given prefix owned by node i.
func (fc *fakeCluster) keyOwnedBy(i int, prefix string) string {
	for n := 0; n < 100000; n++ {
		k := prefix + strconv.Itoa(n)
		fc.mu.Lock()
		o := fc.owner[slotOfKey(k)]
		fc.mu.Unlock()
		if o == i {
			return k
		}
	}
	fc.t.Fatalf("no key owned by node %d", i)
	return ""
}

func (nd *fcNode) kill() {
	nd.ln.Close()
	nd.mu.Lock()
	for c := range nd.conns {
		c.Close()
	}
	nd.mu.Unlock()
}

func (nd *fcNode) logged() []string {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return append([]string(nil), nd.log...)
}

func (nd *fcNode) count(prefix string) int {
	n := 0
	for _, l := range nd.logged() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (nd *fcNode) accept() {
	for {
		c, err := nd.ln.Accept()
		if err != nil {
			return
		}
		nd.mu.Lock()
		nd.conns[c] = struct{}{}
		nd.mu.Unlock()
		go nd.serve(c)
	}
}

type fcConn struct {
	asking, readonly, multi bool
	queued                  [][]string
}

func (nd *fcNode) serve(c net.Conn) {
	defer c.Close()
	r := NewRespReader(c)
	st := &fcConn{}
	for {
		v, err := r.ReadValue()
		if err != nil {
			return
		}
		args := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			args[i] = e.Str
		}
		nd.mu.Lock()
		nd.log = append(nd.log, strings.Join(args, " "))
		nd.mu.Unlock()
		if _, err := c.Write([]byte(nd.handle(st, args))); err != nil {
			return
		}
	}
}

func (nd *fcNode) slotsReply() string {
	fc := nd.fc
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var ranges []string
	start := 0
	for s := 1; s <= clusterSlots; s++ {
		if s == clusterSlots || fc.owner[s] != fc.owner[start] {
			o := fc.owner[start]
			ep := func(i int) string {
				host, port, _ := net.SplitHostPort(fc.addr(i))
				p, _ := strconv.Atoi(port)
				return arr(bulk(host), intr(p), bulk(fmt.Sprintf("node%d", i)))
			}
			items := []string{intr(start), intr(s - 1), ep(o)}
			for _, rep := range fc.replicas[o] {
				items = append(items, ep(rep))
			}
			ranges = append(ranges, arr(items...))
			start = s
		}
	}
	return arr(ranges...)
}

func isKeyed(name string) bool {
	switch name {
	case "GET", "SET", "INCR", "DEL", "MGET", "MSET", "EVAL", "XREAD", "BITOP", "OBJECT":
		return true
	}
	return false
}

func fcKey(args []string) string {
	switch strings.ToUpper(args[0]) {
	case "EVAL":
		return args[3]
	case "XREAD":
		for i, a := range args {
			if strings.EqualFold(a, "STREAMS") {
				return args[i+1]
			}
		}
	case "BITOP":
		return args[2]
	case "OBJECT":
		return args[2]
	}
	return args[1]
}

func (nd *fcNode) handle(st *fcConn, args []string) string {
	fc := nd.fc
	name := strings.ToUpper(args[0])
	switch name {
	case "HELLO":
		return "-ERR unknown command 'HELLO'\r\n"
	case "COMMAND":
		return fakeCommandTable()
	case "PING":
		return "+PONG\r\n"
	case "ASKING":
		st.asking = true
		return "+OK\r\n"
	case "READONLY":
		st.readonly = true
		return "+OK\r\n"
	case "UNWATCH", "WATCH":
		return "+OK\r\n"
	case "CLUSTER":
		if len(args) > 1 && strings.EqualFold(args[1], "SLOTS") {
			return nd.slotsReply()
		}
		return "-ERR unknown subcommand\r\n"
	case "DBSIZE":
		nd.mu.Lock()
		defer nd.mu.Unlock()
		return intr(len(nd.data))
	case "MULTI":
		st.multi = true
		st.queued = nil
		return "+OK\r\n"
	case "EXEC":
		st.multi = false
		q := st.queued
		st.queued = nil
		out := make([]string, len(q))
		for i, qa := range q {
			out[i] = nd.exec(qa)
		}
		return arr(out...)
	}
	if !isKeyed(name) {
		return "-ERR unknown command '" + args[0] + "'\r\n"
	}

	fc.mu.Lock()
	if fc.tryAgain > 0 {
		fc.tryAgain--
		fc.mu.Unlock()
		return "-TRYAGAIN Multiple keys request during rehashing of slot\r\n"
	}
	slot := slotOfKey(fcKey(args))
	owner := fc.owner[slot]
	if fc.alwaysMov {
		next := (nd.idx + 1) % len(fc.nodes)
		fc.mu.Unlock()
		return fmt.Sprintf("-MOVED %d %s\r\n", slot, fc.addr(next))
	}
	mig, migrating := fc.migrating[slot]
	imp, importing := fc.importing[slot]
	isReplica := false
	for _, r := range fc.replicas[owner] {
		if r == nd.idx {
			isReplica = true
		}
	}
	fc.mu.Unlock()

	switch {
	case owner == nd.idx:
		if migrating {
			nd.mu.Lock()
			_, have := nd.data[fcKey(args)]
			nd.mu.Unlock()
			if !have {
				return fmt.Sprintf("-ASK %d %s\r\n", slot, fc.addr(mig))
			}
		}
	case isReplica && st.readonly && (name == "GET" || name == "MGET"):
	case importing && imp == owner && nd.idx != owner && st.asking:
		st.asking = false
	default:
		return fmt.Sprintf("-MOVED %d %s\r\n", slot, fc.addr(owner))
	}
	if st.multi {
		st.queued = append(st.queued, args)
		return "+QUEUED\r\n"
	}
	return nd.exec(args)
}

func (nd *fcNode) exec(args []string) string {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	switch strings.ToUpper(args[0]) {
	case "SET":
		nd.data[args[1]] = args[2]
		return "+OK\r\n"
	case "GET":
		if v, ok := nd.data[args[1]]; ok {
			return bulk(v)
		}
		return "$-1\r\n"
	case "INCR":
		n, _ := strconv.Atoi(nd.data[args[1]])
		n++
		nd.data[args[1]] = strconv.Itoa(n)
		return intr(n)
	case "DEL":
		n := 0
		for _, k := range args[1:] {
			if _, ok := nd.data[k]; ok {
				delete(nd.data, k)
				n++
			}
		}
		return intr(n)
	case "MGET":
		out := make([]string, 0, len(args)-1)
		for _, k := range args[1:] {
			if v, ok := nd.data[k]; ok {
				out = append(out, bulk(v))
			} else {
				out = append(out, "$-1\r\n")
			}
		}
		return arr(out...)
	case "MSET":
		for i := 1; i+1 < len(args); i += 2 {
			nd.data[args[i]] = args[i+1]
		}
		return "+OK\r\n"
	}
	return "+OK\r\n"
}
