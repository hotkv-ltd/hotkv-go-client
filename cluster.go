// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ClusterOptions configures a ClusterClient.
type ClusterOptions struct {
	// Addrs are the seed nodes (host:port). Any one reachable seed is enough
	// to discover the whole cluster.
	Addrs []string

	// Options is the template for the connection to every node: Password,
	// Username, TLSConfig, Protocol, PoolSize (per node), timeouts and so
	// on. Options.Addr is ignored when Addrs is set (and is used as the
	// only seed when it is not). Options.MaxRetries is ignored: each node
	// connection makes a single attempt and the cluster client owns the
	// retry policy (it refreshes the slot map between attempts).
	Options

	// MaxRedirects bounds how many MOVED/ASK redirects one command follows.
	// Default: 5.
	MaxRedirects int

	// ReadOnly lets read-only commands be served by replicas of the owning
	// shard (the replica connections send READONLY). Reads may then return
	// slightly stale data. Writes always go to the primary. Default: false.
	ReadOnly bool
}

// ClusterClient is a cluster-aware client for a HotKV
// cluster. It embeds a Client, so every typed command method (Set, Get,
// HSet, ZAdd, ...) and Do route automatically:
//
//   - the slot map comes from CLUSTER SLOTS, fetched on first use and
//     refreshed when the topology changes (MOVED, CLUSTERDOWN, a failed
//     node);
//   - a command's hash slot is CRC16 of its first key (honoring {hash tags}),
//     with key positions taken from the server's own COMMAND key
//     specifications, so every command routes correctly, typed or not;
//   - MOVED redirects update the slot owner and retry; ASK redirects send
//     ASKING to the target node first; both are bounded by MaxRedirects;
//   - each node has its own connection pool;
//   - keyless commands (PING, INFO, ...) go to a primary node; use
//     ForEachPrimary to run something on every shard;
//   - Pipeline groups commands by node and runs the groups in parallel;
//     TxPipeline needs all its keys in one slot.
//
// Multi-key commands must keep their keys in one slot (use a {hash tag});
// otherwise the server answers CROSSSLOT, exactly as with any cluster
// client.
type ClusterClient struct {
	*Client

	opts ClusterOptions

	mu      sync.RWMutex
	nodes   map[string]*clusterNode
	retired []*clusterNode
	closed  int32

	topo     atomic.Pointer[clusterTopology]
	reloadMu sync.Mutex
	reloadAt atomic.Int64 // unix nanos of the last successful reload
	asyncRe  int32        // an async reload is already scheduled

	overrideMu sync.Mutex
	overrides  map[uint16]*clusterNode // MOVED hints, cleared on reload

	tableMu    sync.Mutex
	table      cmdTable
	tableTried time.Time

	rr uint32
}

type clusterNode struct {
	addr   string
	client *Client
}

type slotRange struct {
	start, end int
	primary    *clusterNode
	replicas   []*clusterNode
}

type clusterTopology struct {
	slots   [clusterSlots]*slotRange
	ranges  []*slotRange
	primary []*clusterNode // distinct primaries in slot order
}

// NewClusterClient creates a cluster client. It does not connect: the first
// command (or ReloadState) discovers the topology from the seeds.
func NewClusterClient(opts *ClusterOptions) (*ClusterClient, error) {
	if opts == nil {
		return nil, errors.New("hotkv: ClusterOptions is required")
	}
	o := *opts
	if len(o.Addrs) == 0 && o.Options.Addr != "" {
		o.Addrs = []string{o.Options.Addr}
	}
	if len(o.Addrs) == 0 {
		return nil, errors.New("hotkv: ClusterOptions.Addrs needs at least one seed address")
	}
	o.Addrs = append([]string(nil), o.Addrs...)
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = 5
	}
	// Keep the caller's retry knob for the cluster loop, but make each node
	// connection a single attempt.
	if o.Options.MaxRetries == 0 {
		o.Options.MaxRetries = 3
	}
	cc := &ClusterClient{
		opts:      o,
		nodes:     make(map[string]*clusterNode),
		overrides: make(map[uint16]*clusterNode),
	}
	base := o.Options
	base.Addr = o.Addrs[0]
	base.init()
	cc.Client = &Client{opts: &base, cluster: cc}
	return cc, nil
}

// ParseClusterURL builds ClusterOptions from one or more seed URLs (the same
// form as ParseURL). Credentials, TLS and the database come from the first
// URL; every URL contributes a seed address.
func ParseClusterURL(rawURLs ...string) (*ClusterOptions, error) {
	if len(rawURLs) == 0 {
		return nil, errors.New("hotkv: at least one cluster URL is required")
	}
	var out *ClusterOptions
	for _, u := range rawURLs {
		o, err := ParseURL(u)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = &ClusterOptions{Options: *o}
		}
		out.Addrs = append(out.Addrs, o.Addr)
	}
	return out, nil
}

// Close closes every node connection.
func (cc *ClusterClient) Close() error {
	if !atomic.CompareAndSwapInt32(&cc.closed, 0, 1) {
		return ErrClosed
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	var first error
	for _, n := range cc.nodes {
		if err := n.client.Close(); err != nil && first == nil {
			first = err
		}
	}
	for _, n := range cc.retired {
		_ = n.client.Close()
	}
	cc.nodes = map[string]*clusterNode{}
	cc.retired = nil
	return first
}

// PoolStats sums the pool statistics of every node.
func (cc *ClusterClient) PoolStats() PoolStats {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	var s PoolStats
	for _, n := range cc.nodes {
		ns := n.client.PoolStats()
		s.Hits += ns.Hits
		s.Misses += ns.Misses
		s.Timeouts += ns.Timeouts
		s.TotalConns += ns.TotalConns
		s.IdleConns += ns.IdleConns
		s.StaleConns += ns.StaleConns
	}
	return s
}

// Watch watches keys on the node that owns them and runs fn with a Tx
// pinned to that node's connection. All keys must hash to one slot.
func (cc *ClusterClient) watch(ctx context.Context, fn func(*Tx) error, keys ...string) error {
	if len(keys) == 0 {
		return errors.New("hotkv: cluster Watch needs at least one key to pick the node")
	}
	if err := cc.ensureTopology(ctx); err != nil {
		return err
	}
	slot := KeySlot(keys[0])
	for _, k := range keys[1:] {
		if KeySlot(k) != slot {
			return NewError("CROSSSLOT Keys in request don't hash to the same slot")
		}
	}
	node := cc.pickNode(slot, false)
	if node == nil {
		return errors.New("hotkv: no node serves the slot")
	}
	return node.client.Watch(ctx, fn, keys...)
}

// ReloadState re-reads the slot map from the cluster.
func (cc *ClusterClient) ReloadState(ctx context.Context) error {
	return cc.reload(ctx)
}

// NodeClient returns a plain single-node Client for the node at addr (as
// reported by the cluster, host:port), or nil if the node is unknown. The
// client is owned by the ClusterClient: do not Close it.
func (cc *ClusterClient) NodeClient(addr string) *Client {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	if n := cc.nodes[addr]; n != nil {
		return n.client
	}
	return nil
}

// SlotAddr returns the address of the primary that currently owns slot, per
// the client's slot map (empty if unknown).
func (cc *ClusterClient) SlotAddr(slot uint16) string {
	t := cc.topo.Load()
	if t == nil || int(slot) >= clusterSlots || t.slots[slot] == nil {
		return ""
	}
	return t.slots[slot].primary.addr
}

// Primaries returns the addresses of the distinct primary nodes, loading the
// topology if needed.
func (cc *ClusterClient) Primaries(ctx context.Context) ([]string, error) {
	if err := cc.ensureTopology(ctx); err != nil {
		return nil, err
	}
	t := cc.topo.Load()
	out := make([]string, len(t.primary))
	for i, n := range t.primary {
		out[i] = n.addr
	}
	return out, nil
}

// ForEachPrimary runs fn once per primary node, concurrently, each with a
// single-node Client for that node (use it for FLUSHALL, DBSIZE, SCAN,
// INFO and other commands that act on one shard). It returns the first
// error any call returned.
func (cc *ClusterClient) ForEachPrimary(ctx context.Context, fn func(ctx context.Context, node *Client) error) error {
	if err := cc.ensureTopology(ctx); err != nil {
		return err
	}
	t := cc.topo.Load()
	return forEachNode(ctx, t.primary, fn)
}

// ForEachNode is ForEachPrimary over every known node, replicas included.
func (cc *ClusterClient) ForEachNode(ctx context.Context, fn func(ctx context.Context, node *Client) error) error {
	if err := cc.ensureTopology(ctx); err != nil {
		return err
	}
	t := cc.topo.Load()
	seen := map[*clusterNode]bool{}
	var all []*clusterNode
	for _, r := range t.ranges {
		for _, n := range append([]*clusterNode{r.primary}, r.replicas...) {
			if !seen[n] {
				seen[n] = true
				all = append(all, n)
			}
		}
	}
	return forEachNode(ctx, all, fn)
}

func forEachNode(ctx context.Context, nodes []*clusterNode, fn func(context.Context, *Client) error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(nodes))
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n *clusterNode) {
			defer wg.Done()
			errs[i] = fn(ctx, n.client)
		}(i, n)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// --- topology ---

func (cc *ClusterClient) isClosed() bool { return atomic.LoadInt32(&cc.closed) == 1 }

// getNode returns (creating on first use) the node for addr.
func (cc *ClusterClient) getNode(addr string) *clusterNode {
	cc.mu.RLock()
	n := cc.nodes[addr]
	cc.mu.RUnlock()
	if n != nil {
		return n
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if n = cc.nodes[addr]; n != nil {
		return n
	}
	o := cc.opts.Options // copy
	o.Addr = addr
	o.MaxRetries = -1 // the cluster loop retries, with a slot map refresh
	if cc.opts.ReadOnly {
		user := o.OnConnect
		o.OnConnect = func(cn *Conn) error {
			if v, err := cn.Do(context.Background(), "READONLY"); err != nil {
				return err
			} else if v.IsErr() {
				return NewError(v.Str)
			}
			if user != nil {
				return user(cn)
			}
			return nil
		}
	}
	n = &clusterNode{addr: addr, client: newClient(&o)}
	cc.nodes[addr] = n
	return n
}

func (cc *ClusterClient) ensureTopology(ctx context.Context) error {
	if cc.isClosed() {
		return ErrClosed
	}
	if cc.topo.Load() != nil {
		return nil
	}
	return cc.reload(ctx)
}

// reload fetches CLUSTER SLOTS from a reachable node and installs the new
// slot map. Concurrent callers coalesce: one that waited on the lock while
// another reload finished returns without a second round trip.
func (cc *ClusterClient) reload(ctx context.Context) error {
	if cc.isClosed() {
		return ErrClosed
	}
	started := time.Now().UnixNano()
	cc.reloadMu.Lock()
	defer cc.reloadMu.Unlock()
	if cc.reloadAt.Load() >= started && cc.topo.Load() != nil {
		return nil
	}

	// Candidates: known primaries first (shuffled), then every other known
	// node, then the seeds.
	var cands []string
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			cands = append(cands, a)
		}
	}
	if t := cc.topo.Load(); t != nil {
		ps := make([]string, 0, len(t.primary))
		for _, n := range t.primary {
			ps = append(ps, n.addr)
		}
		rand.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
		for _, a := range ps {
			add(a)
		}
		for _, r := range t.ranges {
			for _, n := range r.replicas {
				add(n.addr)
			}
		}
	}
	for _, a := range cc.opts.Addrs {
		add(a)
	}

	var lastErr error
	for _, addr := range cands {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		node := cc.getNode(addr)
		val, err := node.client.processOn(ctx, 0, false, []string{"CLUSTER", "SLOTS"})
		if err != nil {
			lastErr = err
			continue
		}
		topo, err := cc.buildTopology(val, addr)
		if err != nil {
			lastErr = err
			continue
		}
		cc.install(topo)
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("hotkv: no cluster node reachable")
	}
	return fmt.Errorf("hotkv: cluster topology unavailable: %w", lastErr)
}

// buildTopology decodes a CLUSTER SLOTS reply. asked is the address of the
// node that answered, used when a node entry carries no host.
func (cc *ClusterClient) buildTopology(v Value, asked string) (*clusterTopology, error) {
	if v.Typ != RespArray {
		return nil, fmt.Errorf("hotkv: unexpected CLUSTER SLOTS reply type %d", v.Typ)
	}
	askedHost, _, _ := net.SplitHostPort(asked)
	endpoint := func(e Value) (string, bool) {
		if (e.Typ != RespArray && e.Typ != RespSet) || len(e.Elems) < 2 {
			return "", false
		}
		host := e.Elems[0].Str
		if e.Elems[0].Typ == RespNull || host == "" || host == "?" {
			host = askedHost
		}
		port := valInt(e.Elems[1])
		if host == "" || port <= 0 {
			return "", false
		}
		return net.JoinHostPort(host, strconv.FormatInt(port, 10)), true
	}

	t := &clusterTopology{}
	seenPrimary := map[*clusterNode]bool{}
	for _, e := range v.Elems {
		if (e.Typ != RespArray && e.Typ != RespSet) || len(e.Elems) < 3 {
			continue
		}
		start, end := int(valInt(e.Elems[0])), int(valInt(e.Elems[1]))
		if start < 0 || end >= clusterSlots || start > end {
			continue
		}
		paddr, ok := endpoint(e.Elems[2])
		if !ok {
			continue
		}
		sr := &slotRange{start: start, end: end, primary: cc.getNode(paddr)}
		for _, r := range e.Elems[3:] {
			if raddr, ok := endpoint(r); ok {
				sr.replicas = append(sr.replicas, cc.getNode(raddr))
			}
		}
		for s := start; s <= end; s++ {
			t.slots[s] = sr
		}
		t.ranges = append(t.ranges, sr)
		if !seenPrimary[sr.primary] {
			seenPrimary[sr.primary] = true
			t.primary = append(t.primary, sr.primary)
		}
	}
	if len(t.ranges) == 0 {
		return nil, errors.New("hotkv: CLUSTER SLOTS returned no slots (cluster mode off or no slots assigned yet)")
	}
	return t, nil
}

func (cc *ClusterClient) install(t *clusterTopology) {
	cc.topo.Store(t)
	cc.reloadAt.Store(time.Now().UnixNano())
	cc.overrideMu.Lock()
	cc.overrides = make(map[uint16]*clusterNode)
	cc.overrideMu.Unlock()

	live := map[string]bool{}
	for _, r := range t.ranges {
		live[r.primary.addr] = true
		for _, n := range r.replicas {
			live[n.addr] = true
		}
	}
	for _, a := range cc.opts.Addrs {
		live[a] = true
	}
	// Close the nodes dropped by the previous reload, retire the ones
	// dropped now (they may still have a command in flight).
	cc.mu.Lock()
	for _, n := range cc.retired {
		_ = n.client.Close()
	}
	cc.retired = nil
	for a, n := range cc.nodes {
		if !live[a] {
			cc.retired = append(cc.retired, n)
			delete(cc.nodes, a)
		}
	}
	cc.mu.Unlock()
}

// reloadAsync schedules a background reload (at most one at a time).
func (cc *ClusterClient) reloadAsync() {
	if !atomic.CompareAndSwapInt32(&cc.asyncRe, 0, 1) {
		return
	}
	go func() {
		defer atomic.StoreInt32(&cc.asyncRe, 0)
		ctx, cancel := context.WithTimeout(context.Background(), cc.opts.Options.DialTimeout+cc.opts.Options.ReadTimeout)
		defer cancel()
		_ = cc.reload(ctx)
	}()
}

// noteMoved records a MOVED hint so the next command for slot goes to its
// new owner at once, and refreshes the slot map in the background.
func (cc *ClusterClient) noteMoved(slot uint16, addr string) {
	n := cc.getNode(addr)
	cc.overrideMu.Lock()
	cc.overrides[slot] = n
	cc.overrideMu.Unlock()
	cc.reloadAsync()
}

// pickNode chooses the node for slot. readOnly lets a replica serve it.
func (cc *ClusterClient) pickNode(slot uint16, readOnly bool) *clusterNode {
	cc.overrideMu.Lock()
	n := cc.overrides[slot]
	cc.overrideMu.Unlock()
	if n != nil {
		return n
	}
	t := cc.topo.Load()
	if t == nil {
		return nil
	}
	sr := t.slots[slot]
	if sr == nil {
		return cc.anyPrimary()
	}
	if readOnly && cc.opts.ReadOnly && len(sr.replicas) > 0 {
		i := int(atomic.AddUint32(&cc.rr, 1)) % (len(sr.replicas) + 1)
		if i < len(sr.replicas) {
			return sr.replicas[i]
		}
	}
	return sr.primary
}

func (cc *ClusterClient) anyPrimary() *clusterNode {
	t := cc.topo.Load()
	if t == nil || len(t.primary) == 0 {
		return nil
	}
	return t.primary[rand.Intn(len(t.primary))]
}

// --- command table ---

// commandTable returns the key-position table from the server, loading it
// once. nil means unavailable: routing then assumes the first argument is
// the key.
func (cc *ClusterClient) commandTable(ctx context.Context) cmdTable {
	cc.tableMu.Lock()
	defer cc.tableMu.Unlock()
	if cc.table != nil {
		return cc.table
	}
	if time.Since(cc.tableTried) < 5*time.Second && !cc.tableTried.IsZero() {
		return nil
	}
	cc.tableTried = time.Now()
	node := cc.anyPrimary()
	if node == nil {
		return nil
	}
	val, err := node.client.processOn(ctx, 0, false, []string{"COMMAND"})
	if err != nil {
		return nil
	}
	t := parseCommandTable(val)
	if len(t) == 0 {
		return nil
	}
	cc.table = t
	return t
}

// slotOf returns the hash slot of a command's first key, and whether the
// command is read-only (per the server's command flags).
func (cc *ClusterClient) slotOf(ctx context.Context, args []string) (slot uint16, hasKey, readOnly bool) {
	if len(args) == 0 {
		return 0, false, false
	}
	if t := cc.commandTable(ctx); t != nil {
		if ci := t.lookup(args); ci != nil {
			idx := ci.keyIndexes(args)
			if len(idx) == 0 {
				return 0, false, ci.readOnly
			}
			return KeySlot(args[idx[0]]), true, ci.readOnly
		}
	}
	// No table, or a command it does not know: the key is the first
	// argument for the overwhelming majority of keyed commands.
	if len(args) > 1 {
		return KeySlot(args[1]), true, false
	}
	return 0, false, false
}

// --- routing ---

func (cc *ClusterClient) route(ctx context.Context, args []string) (Value, error) {
	return cc.routeWith(ctx, args, false, 0)
}

func (cc *ClusterClient) routeBlocking(ctx context.Context, wait time.Duration, args []string) (Value, error) {
	return cc.routeWith(ctx, args, true, wait)
}

func (cc *ClusterClient) retryPause(ctx context.Context, attempt int) error {
	return cc.Client.sleep(ctx, attempt)
}

func (cc *ClusterClient) routeWith(ctx context.Context, args []string, blocking bool, wait time.Duration) (Value, error) {
	if err := cc.ensureTopology(ctx); err != nil {
		return Value{}, err
	}
	slot, hasKey, readOnly := cc.slotOf(ctx, args)
	var node *clusterNode
	if hasKey {
		node = cc.pickNode(slot, readOnly)
	} else {
		node = cc.anyPrimary()
	}

	redirects, failures := 0, 0
	maxFailures := cc.opts.Options.retries()
	asking := false
	var lastErr error
	for {
		if node == nil {
			if err := cc.reload(ctx); err != nil {
				return Value{}, err
			}
			if hasKey {
				node = cc.pickNode(slot, false)
			} else {
				node = cc.anyPrimary()
			}
			if node == nil {
				return Value{}, errors.New("hotkv: no cluster node available")
			}
		}
		if cc.isClosed() {
			return Value{}, ErrClosed
		}

		var val Value
		var err error
		if asking {
			val, err = node.client.processAsking(ctx, args)
		} else {
			val, err = node.client.processOn(ctx, wait, blocking, args)
		}
		asking = false
		if err == nil {
			return val, nil
		}
		lastErr = err

		var re *Error
		if errors.As(err, &re) {
			switch re.prefix {
			case "MOVED":
				if redirects >= cc.opts.MaxRedirects {
					return val, err
				}
				redirects++
				mslot, addr, ok := parseRedirect(re.raw)
				if !ok {
					return val, err
				}
				cc.noteMoved(mslot, hostFallback(addr, node.addr))
				node = cc.getNode(hostFallback(addr, node.addr))
				continue
			case "ASK":
				if redirects >= cc.opts.MaxRedirects {
					return val, err
				}
				redirects++
				_, addr, ok := parseRedirect(re.raw)
				if !ok {
					return val, err
				}
				node = cc.getNode(hostFallback(addr, node.addr))
				asking = true
				continue
			case "TRYAGAIN", "CLUSTERDOWN", "LOADING":
				if failures >= maxFailures {
					return val, err
				}
				failures++
				if re.prefix == "CLUSTERDOWN" {
					cc.reloadAsync()
				}
				if perr := cc.retryPause(ctx, failures); perr != nil {
					return Value{}, perr
				}
				if hasKey {
					node = cc.pickNode(slot, false)
				}
				continue
			}
			return val, err
		}

		// Transport failure (or a pool that was closed because the node left
		// the cluster): the owner may have changed, so refresh and retry.
		if ctx.Err() != nil {
			return Value{}, ctx.Err()
		}
		if cc.isClosed() {
			return Value{}, ErrClosed
		}
		retriable := isRetryableError(err) || errors.Is(err, ErrClosed) || errors.Is(err, ErrPoolTimeout) || isDialError(err)
		if blocking || !retriable || failures >= maxFailures {
			return Value{}, err
		}
		failures++
		if perr := cc.retryPause(ctx, failures); perr != nil {
			return Value{}, perr
		}
		_ = cc.reload(ctx)
		if hasKey {
			node = cc.pickNode(slot, false)
		} else {
			node = cc.anyPrimary()
		}
		_ = lastErr
	}
}

func isDialError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "hotkv: dial ")
}

// parseRedirect parses "MOVED <slot> <host:port>" / "ASK <slot> <host:port>".
func parseRedirect(raw string) (slot uint16, addr string, ok bool) {
	f := strings.Fields(raw)
	if len(f) < 3 {
		return 0, "", false
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n < 0 || n >= clusterSlots {
		return 0, "", false
	}
	return uint16(n), f[2], true
}

// hostFallback fills in the host of a redirect that carries only ":port"
// (a node that does not know its own address) from the node that sent it.
func hostFallback(addr, from string) string {
	if strings.HasPrefix(addr, ":") {
		if h, _, err := net.SplitHostPort(from); err == nil {
			return net.JoinHostPort(h, strings.TrimPrefix(addr, ":"))
		}
	}
	return addr
}

// processAsking sends ASKING and then the command on one connection.
func (c *Client) processAsking(ctx context.Context, args []string) (Value, error) {
	cn, err := c.getConn(ctx)
	if err != nil {
		return Value{}, err
	}
	vals, err := cn.doMulti(ctx, [][]string{{"ASKING"}, args})
	if err != nil {
		c.dropConn(cn)
		return Value{}, err
	}
	c.putConn(cn)
	if vals[0].IsErr() {
		return vals[0], NewError(vals[0].Str)
	}
	if vals[1].IsErr() {
		return vals[1], NewError(vals[1].Str)
	}
	return vals[1], nil
}

// --- pipelines ---

// execPipeline runs queued commands against the cluster. Plain pipelines
// are split by owning node and the groups run in parallel; a command whose
// slot moved is re-routed on its own. Transactional pipelines must keep all
// keys in one slot and run on that slot's owner.
func (cc *ClusterClient) execPipeline(ctx context.Context, cmds []pipelineCmd, tx bool) ([]Cmd, error) {
	if err := cc.ensureTopology(ctx); err != nil {
		for i := range cmds {
			cmds[i].cmd.SetErr(err)
		}
		return collectResults(cmds), err
	}
	if tx {
		return cc.execTx(ctx, cmds)
	}

	type item struct {
		idx  int
		args []string
	}
	groups := map[*clusterNode][]item{}
	for i, pc := range cmds {
		slot, hasKey, readOnly := cc.slotOf(ctx, pc.args)
		var n *clusterNode
		if hasKey {
			n = cc.pickNode(slot, readOnly)
		} else {
			n = cc.anyPrimary()
		}
		if n == nil {
			err := errors.New("hotkv: no cluster node available")
			cmds[i].cmd.SetErr(err)
			continue
		}
		groups[n] = append(groups[n], item{i, pc.args})
	}

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	var retry []int
	var retryMu sync.Mutex
	for n, items := range groups {
		wg.Add(1)
		go func(n *clusterNode, items []item) {
			defer wg.Done()
			batch := make([][]string, len(items))
			for i, it := range items {
				batch[i] = it.args
			}
			cn, err := n.client.getConn(ctx)
			var vals []Value
			if err == nil {
				vals, err = cn.doMulti(ctx, batch)
				if err != nil {
					n.client.dropConn(cn)
				} else {
					n.client.putConn(cn)
				}
			}
			if err != nil {
				// The whole node failed: refresh and run these one by one.
				retryMu.Lock()
				for _, it := range items {
					retry = append(retry, it.idx)
				}
				retryMu.Unlock()
				return
			}
			for i, it := range items {
				v := vals[i]
				if v.IsErr() {
					e := NewError(v.Str)
					if e.prefix == "MOVED" || e.prefix == "ASK" {
						if e.prefix == "MOVED" {
							if s, a, ok := parseRedirect(e.raw); ok {
								cc.noteMoved(s, hostFallback(a, n.addr))
							}
						}
						retryMu.Lock()
						retry = append(retry, it.idx)
						retryMu.Unlock()
						continue
					}
					cmds[it.idx].cmd.SetErr(e)
				}
				cmds[it.idx].cmd.SetVal(v)
			}
		}(n, items)
	}
	wg.Wait()

	if len(retry) > 0 {
		_ = cc.reload(ctx)
		for _, idx := range retry {
			val, err := cc.route(ctx, cmds[idx].args)
			cmds[idx].cmd.SetVal(val)
			cmds[idx].cmd.SetErr(err)
		}
	}
	for _, pc := range cmds {
		if e := pc.cmd.Err(); e != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = e
			}
			errMu.Unlock()
		}
	}
	// Like the single-node pipeline, per-command server errors are reported
	// on each Cmd; only transport/routing errors fail Exec itself.
	var execErr error
	if firstErr != nil {
		var re *Error
		if !errors.As(firstErr, &re) {
			execErr = firstErr
		}
	}
	return collectResults(cmds), execErr
}

func (cc *ClusterClient) execTx(ctx context.Context, cmds []pipelineCmd) ([]Cmd, error) {
	var slot uint16
	have := false
	for _, pc := range cmds {
		s, hasKey, _ := cc.slotOf(ctx, pc.args)
		if !hasKey {
			continue
		}
		if have && s != slot {
			err := NewError("CROSSSLOT Keys in request don't hash to the same slot")
			for i := range cmds {
				cmds[i].cmd.SetErr(err)
			}
			return collectResults(cmds), err
		}
		slot, have = s, true
	}

	var lastErr error
	for attempt := 0; attempt <= cc.opts.MaxRedirects; attempt++ {
		var node *clusterNode
		if have {
			node = cc.pickNode(slot, false)
		} else {
			node = cc.anyPrimary()
		}
		if node == nil {
			return collectResults(cmds), errors.New("hotkv: no cluster node available")
		}
		// Reset results of a previous attempt.
		sub := &Pipeline{client: node.client, tx: true}
		for _, pc := range cmds {
			pc.cmd.SetErr(nil)
			pc.cmd.SetVal(Value{})
			sub.cmds = append(sub.cmds, pc)
		}
		_, err := sub.Exec(ctx)
		lastErr = err
		redir := false
		for _, pc := range cmds {
			var re *Error
			if errors.As(pc.cmd.Err(), &re) && (re.prefix == "MOVED") {
				if s, a, ok := parseRedirect(re.raw); ok {
					cc.noteMoved(s, hostFallback(a, node.addr))
				}
				redir = true
				break
			}
		}
		if redir {
			continue
		}
		if err != nil && (isRetryableError(err) || isDialError(err)) && ctx.Err() == nil {
			_ = cc.reload(ctx)
			continue
		}
		return collectResults(cmds), err
	}
	return collectResults(cmds), lastErr
}
