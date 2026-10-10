// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"math"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

func TestLiveTagsAndNanoTTL(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		key, a, b := k("tagged"), k("alpha"), k("beta")
		ck(t, c.Set(ctx, key, "v", 0).Err(), "SET")
		n, err := c.TagSet(ctx, key, a, b).Result()
		ck(t, err, "TAG.SET")
		eq(t, "TAG.SET", n, int64(2))
		tags, _ := c.TagGet(ctx, key).Result()
		eqUnordered(t, "TAG.GET", tags, []string{a, b})
		mem, _ := c.TagMembers(ctx, a).Result()
		eq(t, "TAG.MEMBERS", mem, []string{key})
		ck(t, c.Set(ctx, k("other"), "v", 0).Err(), "SET other")
		c.TagSet(ctx, k("other"), a)
		inter, _ := c.TagInter(ctx, a, b).Result()
		eq(t, "TAG.INTER", inter, []string{key})
		cnt, _ := c.TagCount(ctx, key).Result()
		eq(t, "TAG.COUNT", cnt, int64(2))
		ex, _ := c.TagExists(ctx, key, a).Result()
		eq(t, "TAG.EXISTS yes", ex, true)
		ex, _ = c.TagExists(ctx, key, k("nope")).Result()
		eq(t, "TAG.EXISTS no", ex, false)
		rem, _ := c.TagRem(ctx, key, a).Result()
		eq(t, "TAG.REM", rem, int64(1))
		mem, _ = c.TagMembers(ctx, a).Result()
		eq(t, "TAG.MEMBERS after REM", mem, []string{k("other")})

		// nanosecond TTLs
		ok, err := c.NExpire(ctx, key, int64(5*time.Second)).Result()
		ck(t, err, "NEXPIRE")
		eq(t, "NEXPIRE", ok, true)
		ttl, err := c.NTTL(ctx, key).Result()
		ck(t, err, "NTTL")
		if ttl <= 3*time.Second || ttl > 5*time.Second {
			t.Errorf("NTTL = %v, want about 5s", ttl)
		}
		at := time.Now().Add(time.Hour).UnixNano()
		ok, _ = c.NExpireAt(ctx, key, at).Result()
		eq(t, "NEXPIREAT", ok, true)
		ttl, _ = c.NTTL(ctx, key).Result()
		if ttl < 59*time.Minute || ttl > time.Hour {
			t.Errorf("NTTL after NEXPIREAT = %v, want about 1h", ttl)
		}
		ttl, _ = c.NTTL(ctx, k("missing")).Result()
		eq(t, "NTTL missing", ttl, time.Duration(-2))
		ok, _ = c.NExpire(ctx, k("missing"), 1).Result()
		eq(t, "NEXPIRE missing", ok, false)
		// a nanosecond expiry really expires
		ck(t, c.Set(ctx, k("short"), "v", 0).Err(), "SET short")
		c.NExpire(ctx, k("short"), int64(50*time.Millisecond))
		time.Sleep(150 * time.Millisecond)
		_, err = c.Get(ctx, k("short")).Result()
		isNil(t, "GET after the NEXPIRE elapsed", err)
	})
}

func TestLiveHotKeysAndLatency(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		cfg, _ := c.ConfigGet(ctx, "hotkey-tracking").Result()
		oldHK := cfg["hotkey-tracking"]
		ck(t, c.ConfigSet(ctx, "hotkey-tracking", "yes").Err(), "CONFIG SET hotkey-tracking")
		defer c.ConfigSet(ctx, "hotkey-tracking", oldHK)
		ck(t, c.HotKeysReset(ctx).Err(), "HOTKEYS RESET")
		hot := k("hot")
		c.Set(ctx, hot, "v", 0)
		for i := 0; i < 50; i++ {
			c.Get(ctx, hot)
		}
		top, err := c.HotKeysTop(ctx, 3).Result()
		ck(t, err, "HOTKEYS TOP")
		if len(top) == 0 || top[0].Key != hot || top[0].Count < 50 {
			t.Errorf("HOTKEYS TOP = %+v, want %s first with count >= 50", top, hot)
		}
		ck(t, c.HotKeysReset(ctx).Err(), "HOTKEYS RESET 2")
		top, _ = c.HotKeysTop(ctx, 3).Result()
		eq(t, "HOTKEYS TOP after RESET", len(top), 0)

		lc, _ := c.ConfigGet(ctx, "latency-monitor-threshold").Result()
		oldLat := lc["latency-monitor-threshold"]
		ck(t, c.ConfigSet(ctx, "latency-monitor-threshold", "2").Err(), "CONFIG SET latency threshold")
		defer c.ConfigSet(ctx, "latency-monitor-threshold", oldLat)
		ck(t, c.LatencyReset(ctx).Err(), "LATENCY RESET")
		// a Lua busy loop is the portable way to produce a slow command
		slow := c.Do(ctx, "EVAL", "local x=0 for i=1,40000000 do x=x+i end return x", 0)
		ck(t, slow.Err(), "EVAL busy loop")
		latest, err := c.LatencyLatest(ctx).Result()
		ck(t, err, "LATENCY LATEST")
		fl := flat(latest...)
		if !has(fl, "command") && !has(fl, "eval") {
			t.Errorf("LATENCY LATEST = %v, want a command/eval event", fl)
		}
		hist, err := c.LatencyHistory(ctx, "command").Result()
		ck(t, err, "LATENCY HISTORY")
		if len(hist) == 0 {
			t.Errorf("LATENCY HISTORY command is empty after a slow command")
		}
		ck(t, c.LatencyReset(ctx).Err(), "LATENCY RESET 2")
		latest, _ = c.LatencyLatest(ctx).Result()
		eq(t, "LATENCY LATEST after RESET", len(latest), 0)
	})
}

func TestLiveVSIM(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		idx := k("vidx")
		ck(t, c.VSIMCreateIndex(ctx, idx, 3, &hotkv.VSIMIndexOptions{Metric: "cosine"}).Err(), "VSIM.CREATEINDEX")
		ck(t, c.VSIMSet(ctx, idx, "a", []float32{1, 0, 0}).Err(), "VSIM.SET a")
		ck(t, c.VSIMSet(ctx, idx, "b", []float32{0, 1, 0}).Err(), "VSIM.SET b")
		ck(t, c.VSIMSet(ctx, idx, "c", []float32{0.9, 0.1, 0}).Err(), "VSIM.SET c")
		got, err := c.VSIMGet(ctx, idx, "c").Result()
		ck(t, err, "VSIM.GET")
		// a cosine index stores unit vectors
		if len(got) != 3 {
			t.Fatalf("VSIM.GET = %v", got)
		}
		norm := math.Sqrt(0.81 + 0.01)
		near(t, "VSIM.GET x", float64(got[0]), 0.9/norm, 1e-5)
		near(t, "VSIM.GET y", float64(got[1]), 0.1/norm, 1e-5)
		near(t, "VSIM.GET z", float64(got[2]), 0, 1e-9)
		res, err := c.VSIMSearch(ctx, idx, []float32{1, 0, 0}, 2, nil).Result()
		ck(t, err, "VSIM.SEARCH")
		if len(res) != 2 || res[0].Key != "a" || res[1].Key != "c" {
			t.Fatalf("VSIM.SEARCH = %+v, want a then c", res)
		}
		near(t, "distance to itself", res[0].Score, 0, 1e-6)
		if res[1].Score <= 0 {
			t.Errorf("distance to c = %v, want > 0", res[1].Score)
		}
		info, err := c.VSIMInfo(ctx, idx).Result()
		ck(t, err, "VSIM.INFO")
		if info.Name != idx || info.Dimensions != 3 || info.Metric != "cosine" || info.Vectors != 3 {
			t.Errorf("VSIM.INFO = %+v", info)
		}
		names, err := c.VSIMIndexNames(ctx).Result()
		ck(t, err, "VSIM.INFO (all)")
		if !has(names, idx) {
			t.Errorf("VSIMIndexNames = %v, missing %s", names, idx)
		}
		// tiny and huge components must be written as plain decimals (an L2
		// index keeps the vector as given, a cosine index normalises it)
		l2 := k("vl2")
		ck(t, c.VSIMCreateIndex(ctx, l2, 3, &hotkv.VSIMIndexOptions{Metric: "l2"}).Err(), "VSIM.CREATEINDEX l2")
		defer c.VSIMDropIndex(ctx, l2)
		ck(t, c.VSIMSet(ctx, l2, "tiny", []float32{1e-5, 1e-12, 3e5}).Err(), "VSIM.SET tiny")
		tv, _ := c.VSIMGet(ctx, l2, "tiny").Result()
		eq(t, "VSIM.GET tiny", tv, []float32{1e-5, 1e-12, 3e5})
		if err := c.VSIMSet(ctx, idx, "bad", []float32{float32(math.NaN()), 0, 0}).Err(); err == nil {
			t.Error("NaN component accepted")
		}
		del, _ := c.VSIMDel(ctx, idx, "a").Result()
		eq(t, "VSIM.DEL", del, true)
		del, _ = c.VSIMDel(ctx, idx, "a").Result()
		eq(t, "VSIM.DEL again", del, false)
		ck(t, c.VSIMDropIndex(ctx, idx).Err(), "VSIM.DROPINDEX")
		if err := c.VSIMInfo(ctx, idx).Err(); err == nil {
			t.Error("VSIM.INFO of a dropped index must fail")
		}
	})
}

func TestLiveVSIMPersistence(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		// snapshot names may not contain ':' or '.'
		name := "gosdk_" + sanitize(k("snap"))
		blob := []byte("snapshot\x00bytes\r\n\xff")
		n, err := c.VSIMSave(ctx, name, blob).Result()
		ck(t, err, "VSIM.SAVE")
		if n < int64(len(blob)) {
			t.Errorf("VSIM.SAVE bytes = %d", n)
		}
		got, err := c.VSIMLoad(ctx, name).Result()
		ck(t, err, "VSIM.LOAD")
		eq(t, "VSIM.LOAD", got, string(blob))
		names, err := c.VSIMSnapshots(ctx).Result()
		ck(t, err, "VSIM.LIST")
		if !has(names, name) {
			t.Errorf("VSIM.LIST = %v, missing %s", names, name)
		}
		ok, err := c.VSIMSnapshotDrop(ctx, name).Result()
		ck(t, err, "VSIM.DROP")
		eq(t, "VSIM.DROP", ok, true)
		if v, err := c.VSIMLoad(ctx, name).Result(); err == nil {
			t.Errorf("VSIM.LOAD of a dropped snapshot returned %q, want an error or Nil", v)
		}
		if err := c.VSIMSave(ctx, "bad:name", blob).Err(); err == nil {
			t.Error("a snapshot name containing ':' must be rejected")
		}
	})
}

func sanitize(s string) string {
	out := []byte(s)
	for i, ch := range out {
		if ch == ':' || ch == '.' || ch == '-' {
			out[i] = '_'
		}
	}
	return string(out)
}

func TestLiveGCRA(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		key := k("gcra")
		// burst 2: two immediate requests pass, the third is denied
		r1, err := c.GCRALimit(ctx, key, time.Second, 2, 0).Result()
		ck(t, err, "GCRA.LIMIT")
		eq(t, "first request denied", r1.Denied, false)
		eq(t, "limit", r1.Limit, int64(2))
		eq(t, "remaining after first", r1.Remaining, int64(1))
		eq(t, "retry-after when allowed", r1.RetryAfterMs, int64(-1))
		r2, _ := c.GCRALimit(ctx, key, time.Second, 2, 0).Result()
		eq(t, "second request denied", r2.Denied, false)
		eq(t, "remaining after second", r2.Remaining, int64(0))
		r3, _ := c.GCRALimit(ctx, key, time.Second, 2, 0).Result()
		eq(t, "third request denied", r3.Denied, true)
		if r3.RetryAfterMs <= 0 || r3.RetryAfterMs > 1000 {
			t.Errorf("retry-after = %dms", r3.RetryAfterMs)
		}
		n, err := c.GCRAReset(ctx, key).Result()
		ck(t, err, "GCRA.RESET")
		eq(t, "GCRA.RESET", n, int64(1))
		r4, _ := c.GCRALimit(ctx, key, time.Second, 2, 0).Result()
		eq(t, "denied after reset", r4.Denied, false)
		c.GCRAReset(ctx, key)
	})
}

func TestLiveSemanticCache(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		ns := k("scache")
		th := 0.5
		ck(t, c.SCacheCreate(ctx, ns, 3, &hotkv.SCacheCreateOptions{Metric: "cosine", Threshold: &th}).Err(), "SCACHE.CREATE")
		defer c.SCacheDrop(ctx, ns)
		usage := &hotkv.Usage{Model: "gpt-4", Input: 12, Output: 8}
		ck(t, c.SCacheSet(ctx, ns, "q1", "r1", []float32{0.1, 0.2, 0.3}, 0, usage).Err(), "SCACHE.SET")
		res, err := c.SCacheGet(ctx, ns, []float32{0.1, 0.2, 0.31}, nil).Result()
		ck(t, err, "SCACHE.GET similar")
		if !res.Hit || res.Response != "r1" || res.Similarity < 0.99 {
			t.Errorf("similar lookup = %+v, want a hit of r1", res)
		}
		res, _ = c.SCacheGet(ctx, ns, []float32{0.9, 0, 0}, nil).Result()
		eq(t, "dissimilar lookup is a miss", res.Hit, false)
		// The THRESHOLD override is sent (the wire format is covered by a unit
		// test) and accepted. How it affects the lookup can differ between
		// server versions, so only acceptance is asserted here.
		hi := 0.99999999
		_, err = c.SCacheGet(ctx, ns, []float32{0.1, 0.2, 0.31}, &hi).Result()
		ck(t, err, "SCACHE.GET with THRESHOLD")
		st, err := c.SCacheStats(ctx, ns).Result()
		ck(t, err, "SCACHE.STATS")
		eq(t, "entries", st.Entries, int64(1))
		eq(t, "hits", st.Hits, int64(2))
		eq(t, "misses", st.Misses, int64(1))
		eq(t, "usage saved input tokens", st.Usage.InputTokens, int64(24))
		eq(t, "usage saved output tokens", st.Usage.OutputTokens, int64(16))
		ck(t, c.SCacheThreshold(ctx, ns, 1e-5).Err(), "SCACHE.THRESHOLD tiny")
		ck(t, c.SCacheThreshold(ctx, ns, 0.75).Err(), "SCACHE.THRESHOLD")
		list, err := c.SCacheList(ctx).Result()
		ck(t, err, "SCACHE.LIST")
		if !has(list, ns) {
			t.Errorf("SCACHE.LIST = %v", list)
		}
		fl, err := c.SCacheFlush(ctx, ns).Result()
		ck(t, err, "SCACHE.FLUSH")
		eq(t, "SCACHE.FLUSH", fl, true)
		st, _ = c.SCacheStats(ctx, ns).Result()
		eq(t, "entries after flush", st.Entries, int64(0))
		dr, err := c.SCacheDrop(ctx, ns).Result()
		ck(t, err, "SCACHE.DROP")
		eq(t, "SCACHE.DROP", dr, true)
		again, _ := c.SCacheDrop(ctx, ns).Result()
		eq(t, "SCACHE.DROP of a dropped namespace", again, false)
	})
}

func TestLivePromptCache(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		ns := k("pcache")
		ck(t, c.PCacheCreate(ctx, ns, 100, time.Hour, 1e-5).Err(), "PCACHE.CREATE")
		defer c.PCacheDrop(ctx, ns)
		usage := &hotkv.Usage{Model: "gpt-4", Input: 12, Output: 8}
		ck(t, c.PCacheSet(ctx, ns, "p1", "r1", 0, "meta1", usage).Err(), "PCACHE.SET")
		ck(t, c.PCacheSet(ctx, ns, "p2", "r2", time.Minute, "", nil).Err(), "PCACHE.SET ttl")
		v, err := c.PCacheGet(ctx, ns, "p1").Result()
		ck(t, err, "PCACHE.GET")
		eq(t, "PCACHE.GET", v, "r1")
		_, err = c.PCacheGet(ctx, ns, "none").Result()
		isNil(t, "PCACHE.GET miss", err)
		ex, err := c.PCacheGetEx(ctx, ns, "p1").Result()
		ck(t, err, "PCACHE.GETEX")
		fl := flat(ex...)
		if len(fl) < 3 || fl[0] != "r1" || fl[1] != "meta1" {
			t.Errorf("PCACHE.GETEX = %v, want response, meta, ...", fl)
		}
		ck(t, c.PCacheConfigSet(ctx, ns, "TTL", "200").Err(), "PCACHE.CONFIG SET TTL")
		ck(t, c.PCacheConfigSet(ctx, ns, "COST_PER_HIT", "0.000001").Err(), "PCACHE.CONFIG SET COST_PER_HIT")
		st, err := c.PCacheStats(ctx, ns).Result()
		ck(t, err, "PCACHE.STATS")
		eq(t, "entries", st.Entries, int64(2))
		eq(t, "hits", st.Hits, int64(2))
		eq(t, "misses", st.Misses, int64(1))
		eq(t, "usage saved input tokens", st.Usage.InputTokens, int64(24))
		list, _ := c.PCacheList(ctx).Result()
		if !has(list, ns) {
			t.Errorf("PCACHE.LIST = %v", list)
		}
		del, err := c.PCacheDel(ctx, ns, "p2").Result()
		ck(t, err, "PCACHE.DEL")
		eq(t, "PCACHE.DEL", del, true)
		del, _ = c.PCacheDel(ctx, ns, "p2").Result()
		eq(t, "PCACHE.DEL again", del, false)
		n, err := c.PCacheFlush(ctx, ns).Result()
		ck(t, err, "PCACHE.FLUSH")
		eq(t, "PCACHE.FLUSH removed", n, int64(1))
		dr, _ := c.PCacheDrop(ctx, ns).Result()
		eq(t, "PCACHE.DROP", dr, true)
	})
}

func TestLiveKeyedCaches(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		type fam struct {
			name   string
			create func(context.Context, string, uint64, time.Duration) *hotkv.StatusCmd
			set    func(context.Context, string, string, string, time.Duration, string, *hotkv.Usage) *hotkv.StatusCmd
			get    func(context.Context, string, string) *hotkv.StringCmd
			del    func(context.Context, string, string) *hotkv.BoolCmd
			stats  func(context.Context, string) *hotkv.KeyedCacheStatsCmd
			flush  func(context.Context, string) *hotkv.IntCmd
			drop   func(context.Context, string) *hotkv.BoolCmd
			list   func(context.Context) *hotkv.StringSliceCmd
		}
		fams := []fam{
			{"TOOL.CACHE", c.ToolCacheCreate, c.ToolCacheSet, c.ToolCacheGet, c.ToolCacheDel, c.ToolCacheStats, c.ToolCacheFlush, c.ToolCacheDrop, c.ToolCacheList},
			{"NCACHE", c.NCacheCreate, c.NCacheSet, c.NCacheGet, c.NCacheDel, c.NCacheStats, c.NCacheFlush, c.NCacheDrop, c.NCacheList},
			{"GUARD", c.GuardCacheCreate, c.GuardCacheSet, c.GuardCacheGet, c.GuardCacheDel, c.GuardCacheStats, c.GuardCacheFlush, c.GuardCacheDrop, c.GuardCacheList},
			{"RERANK", c.RerankCacheCreate, c.RerankCacheSet, c.RerankCacheGet, c.RerankCacheDel, c.RerankCacheStats, c.RerankCacheFlush, c.RerankCacheDrop, c.RerankCacheList},
		}
		for _, f := range fams {
			f := f
			t.Run(f.name, func(t *testing.T) {
				ns := k(f.name)
				ck(t, f.create(ctx, ns, 100, time.Hour).Err(), f.name+".CREATE")
				defer f.drop(ctx, ns)
				ck(t, f.set(ctx, ns, "k1", "v1", 0, "m", &hotkv.Usage{Model: "gpt-4", Input: 5}).Err(), "SET")
				ck(t, f.set(ctx, ns, "k2", "v2\r\nbinary\x00", time.Minute, "", nil).Err(), "SET 2")
				v, err := f.get(ctx, ns, "k1").Result()
				ck(t, err, "GET")
				eq(t, "GET", v, "v1")
				v, _ = f.get(ctx, ns, "k2").Result()
				eq(t, "GET binary", v, "v2\r\nbinary\x00")
				_, err = f.get(ctx, ns, "none").Result()
				isNil(t, "GET miss", err)
				st, err := f.stats(ctx, ns).Result()
				ck(t, err, "STATS")
				eq(t, "entries", st.Entries, int64(2))
				eq(t, "hits", st.Hits, int64(2))
				eq(t, "misses", st.Misses, int64(1))
				eq(t, "usage saved input tokens", st.Usage.InputTokens, int64(5)) // only k1 carried usage
				list, _ := f.list(ctx).Result()
				if !has(list, ns) {
					t.Errorf("LIST = %v", list)
				}
				del, _ := f.del(ctx, ns, "k2").Result()
				eq(t, "DEL", del, true)
				del, _ = f.del(ctx, ns, "k2").Result()
				eq(t, "DEL again", del, false)
				n, _ := f.flush(ctx, ns).Result()
				eq(t, "FLUSH removed", n, int64(1))
				dr, _ := f.drop(ctx, ns).Result()
				eq(t, "DROP", dr, true)
			})
		}
	})
}
