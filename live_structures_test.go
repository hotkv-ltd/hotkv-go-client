// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

func TestLiveLists(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		l := k("list")
		n, err := c.RPush(ctx, l, "a", "b", "c").Result()
		ck(t, err, "RPUSH")
		eq(t, "RPUSH", n, int64(3))
		n, _ = c.LPush(ctx, l, "z", 0).Result()
		eq(t, "LPUSH", n, int64(5))
		s, _ := c.LRange(ctx, l, 0, -1).Result()
		eq(t, "LRANGE", s, []string{"0", "z", "a", "b", "c"})
		n, _ = c.LLen(ctx, l).Result()
		eq(t, "LLEN", n, int64(5))
		v, _ := c.LIndex(ctx, l, 2).Result()
		eq(t, "LINDEX", v, "a")
		_, err = c.LIndex(ctx, l, 99).Result()
		isNil(t, "LINDEX out of range", err)
		ck(t, c.LSet(ctx, l, 1, "Z").Err(), "LSET")
		v, _ = c.LIndex(ctx, l, 1).Result()
		eq(t, "after LSET", v, "Z")
		n, _ = c.LInsertBefore(ctx, l, "a", "before-a").Result()
		eq(t, "LINSERT BEFORE", n, int64(6))
		n, _ = c.LInsertAfter(ctx, l, "a", "after-a").Result()
		eq(t, "LINSERT AFTER", n, int64(7))
		s, _ = c.LRange(ctx, l, 0, -1).Result()
		eq(t, "after LINSERTs", s, []string{"0", "Z", "before-a", "a", "after-a", "b", "c"})
		n, _ = c.LRem(ctx, l, 0, "before-a").Result()
		eq(t, "LREM", n, int64(1))
		v, _ = c.LPop(ctx, l).Result()
		eq(t, "LPOP", v, "0")
		v, _ = c.RPop(ctx, l).Result()
		eq(t, "RPOP", v, "c")
		_, err = c.LPop(ctx, k("list-none")).Result()
		isNil(t, "LPOP on a missing list", err)

		// push-if-exists
		n, _ = c.LPushX(ctx, k("list-none"), "x").Result()
		eq(t, "LPUSHX missing", n, int64(0))
		n, _ = c.RPushX(ctx, k("list-none"), "x").Result()
		eq(t, "RPUSHX missing", n, int64(0))
		n, _ = c.LPushX(ctx, l, "head").Result()
		eq(t, "LPUSHX existing", n, int64(5))
		n, _ = c.RPushX(ctx, l, "tail").Result()
		eq(t, "RPUSHX existing", n, int64(6))

		// blocking pops: immediate hit, then a real wait that times out
		bl := k("blist")
		ck(t, c.RPush(ctx, bl, "one", "two").Err(), "RPUSH bl")
		kv, err := c.BLPop(ctx, time.Second, bl).Result()
		ck(t, err, "BLPOP")
		eq(t, "BLPOP", kv, []string{bl, "one"})
		kv, err = c.BRPop(ctx, time.Second, bl).Result()
		ck(t, err, "BRPOP")
		eq(t, "BRPOP", kv, []string{bl, "two"})
		start := time.Now()
		_, err = c.BLPop(ctx, 1500*time.Millisecond, bl).Result()
		el := time.Since(start)
		isNil(t, "BLPOP timeout", err)
		if el < time.Second || el > 4*time.Second {
			t.Errorf("BLPOP 1.5s timeout took %v (sub-second precision / ReadTimeout interplay)", el)
		}
		// a timeout longer than the client's ReadTimeout must still work
		opts := *c.Options()
		opts.ReadTimeout = 500 * time.Millisecond
		opts.MaxRetries = -1
		short, err := hotkv.NewClient(&opts)
		ck(t, err, "NewClient short ReadTimeout")
		defer short.Close()
		go func() {
			time.Sleep(900 * time.Millisecond)
			c.RPush(ctx, bl, "late")
		}()
		kv, err = short.BLPop(ctx, 5*time.Second, bl).Result()
		ck(t, err, "BLPOP longer than ReadTimeout")
		eq(t, "BLPOP woken by a later push", kv, []string{bl, "late"})
		// sub-second timeout must not become 0 (= block forever)
		start = time.Now()
		_, err = c.BLPop(ctx, 300*time.Millisecond, bl).Result()
		isNil(t, "BLPOP 300ms", err)
		if time.Since(start) > 3*time.Second {
			t.Errorf("BLPOP 300ms blocked for %v", time.Since(start))
		}
	})
}

func TestLiveSets(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		a, b := k("sa"), k("sb")
		n, err := c.SAdd(ctx, a, "1", "2", "3", 4).Result()
		ck(t, err, "SADD")
		eq(t, "SADD", n, int64(4))
		n, _ = c.SAdd(ctx, a, "1").Result()
		eq(t, "SADD duplicate", n, int64(0))
		ck(t, c.SAdd(ctx, b, "3", "4", "5").Err(), "SADD b")
		m, _ := c.SMembers(ctx, a).Result()
		eqUnordered(t, "SMEMBERS", m, []string{"1", "2", "3", "4"})
		card, _ := c.SCard(ctx, a).Result()
		eq(t, "SCARD", card, int64(4))
		is, _ := c.SIsMember(ctx, a, "2").Result()
		eq(t, "SISMEMBER yes", is, true)
		is, _ = c.SIsMember(ctx, a, "9").Result()
		eq(t, "SISMEMBER no", is, false)
		mis, err := c.SMIsMember(ctx, a, "1", "9", "4").Result()
		ck(t, err, "SMISMEMBER")
		if len(mis) != 3 || mis[0].Int != 1 || mis[1].Int != 0 || mis[2].Int != 1 {
			t.Errorf("SMISMEMBER = %+v", mis)
		}
		u, _ := c.SUnion(ctx, a, b).Result()
		eqUnordered(t, "SUNION", u, []string{"1", "2", "3", "4", "5"})
		i, _ := c.SInter(ctx, a, b).Result()
		eqUnordered(t, "SINTER", i, []string{"3", "4"})
		d, _ := c.SDiff(ctx, a, b).Result()
		eqUnordered(t, "SDIFF", d, []string{"1", "2"})
		n, _ = c.SUnionStore(ctx, k("su"), a, b).Result()
		eq(t, "SUNIONSTORE", n, int64(5))
		n, _ = c.SInterStore(ctx, k("si"), a, b).Result()
		eq(t, "SINTERSTORE", n, int64(2))
		n, _ = c.SDiffStore(ctx, k("sd"), a, b).Result()
		eq(t, "SDIFFSTORE", n, int64(2))
		rm, _ := c.SRandMemberN(ctx, a, 2).Result()
		if len(rm) != 2 {
			t.Errorf("SRANDMEMBER 2 = %v", rm)
		}
		r1, err := c.SRandMember(ctx, a).Result()
		ck(t, err, "SRANDMEMBER")
		if r1 == "" {
			t.Error("SRANDMEMBER empty")
		}
		var seen []string
		var cur uint64
		for x := 0; x < 100; x++ {
			nc, ks, err := c.SScan(ctx, a, cur, "", 100).Result()
			ck(t, err, "SSCAN")
			seen = append(seen, ks...)
			if cur = nc; cur == 0 {
				break
			}
		}
		eqUnordered(t, "SSCAN", dedupe(seen), []string{"1", "2", "3", "4"})
		pop, _ := c.SPopN(ctx, a, 2).Result()
		if len(pop) != 2 {
			t.Errorf("SPOP 2 = %v", pop)
		}
		p1, err := c.SPop(ctx, a).Result()
		ck(t, err, "SPOP")
		if p1 == "" {
			t.Error("SPOP empty")
		}
		n, _ = c.SRem(ctx, b, "3", "99").Result()
		eq(t, "SREM", n, int64(1))
		_, err = c.SPop(ctx, k("s-none")).Result()
		isNil(t, "SPOP on a missing set", err)
	})
}

func TestLiveHashes(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		h := k("h")
		n, err := c.HSet(ctx, h, "f1", "v1", "f2", 2, "f3", []byte("v3")).Result()
		ck(t, err, "HSET")
		eq(t, "HSET", n, int64(3))
		v, _ := c.HGet(ctx, h, "f1").Result()
		eq(t, "HGET", v, "v1")
		_, err = c.HGet(ctx, h, "none").Result()
		isNil(t, "HGET missing", err)
		all, _ := c.HGetAll(ctx, h).Result()
		eq(t, "HGETALL", all, map[string]string{"f1": "v1", "f2": "2", "f3": "v3"})
		keys, _ := c.HKeys(ctx, h).Result()
		eqUnordered(t, "HKEYS", keys, []string{"f1", "f2", "f3"})
		vals, _ := c.HVals(ctx, h).Result()
		eqUnordered(t, "HVALS", vals, []string{"v1", "2", "v3"})
		ln, _ := c.HLen(ctx, h).Result()
		eq(t, "HLEN", ln, int64(3))
		ex, _ := c.HExists(ctx, h, "f1").Result()
		eq(t, "HEXISTS yes", ex, true)
		ex, _ = c.HExists(ctx, h, "none").Result()
		eq(t, "HEXISTS no", ex, false)
		ok, _ := c.HSetNX(ctx, h, "f1", "x").Result()
		eq(t, "HSETNX existing", ok, false)
		ok, _ = c.HSetNX(ctx, h, "f4", "new").Result()
		eq(t, "HSETNX new", ok, true)
		in, _ := c.HIncrBy(ctx, h, "f2", 40).Result()
		eq(t, "HINCRBY", in, int64(42))
		fl, err := c.HIncrByFloat(ctx, h, "fl", 0.5).Result()
		ck(t, err, "HINCRBYFLOAT")
		near(t, "HINCRBYFLOAT", fl, 0.5, 1e-9)
		fl, _ = c.HIncrByFloat(ctx, h, "fl", 1e-5).Result()
		near(t, "HINCRBYFLOAT tiny", fl, 0.50001, 1e-9)
		mg, err := c.HMGet(ctx, h, "f1", "none", "f2").Result()
		ck(t, err, "HMGET")
		if len(mg) != 3 || mg[0].Str != "v1" || !mg[1].IsNil() || mg[2].Str != "42" {
			t.Errorf("HMGET = %+v", mg)
		}
		rf, err := c.HRandField(ctx, h).Result()
		ck(t, err, "HRANDFIELD")
		if _, ok := all[rf]; !ok && rf != "f4" && rf != "fl" {
			t.Errorf("HRANDFIELD = %q", rf)
		}
		rn, _ := c.HRandFieldN(ctx, h, 2).Result()
		if len(rn) != 2 {
			t.Errorf("HRANDFIELD 2 = %v", rn)
		}
		rv, err := c.HRandFieldWithValues(ctx, h, 2).Result()
		ck(t, err, "HRANDFIELD WITHVALUES")
		if len(rv) != 2 || rv[0].Key == "" {
			t.Errorf("HRANDFIELD WITHVALUES = %+v", rv)
		}
		var seen []string
		var cur uint64
		for x := 0; x < 100; x++ {
			nc, ks, err := c.HScan(ctx, h, cur, "f*", 100).Result()
			ck(t, err, "HSCAN")
			seen = append(seen, ks...)
			if cur = nc; cur == 0 {
				break
			}
		}
		// HSCAN returns field, value, field, value...
		got := map[string]string{}
		for i := 0; i+1 < len(seen); i += 2 {
			got[seen[i]] = seen[i+1]
		}
		if got["f1"] != "v1" || got["f2"] != "42" || got["f3"] != "v3" {
			t.Errorf("HSCAN pairs = %v", got)
		}
		d, _ := c.HDel(ctx, h, "f1", "f2", "none").Result()
		eq(t, "HDEL", d, int64(2))
		ln, _ = c.HLen(ctx, h).Result()
		eq(t, "HLEN after HDEL", ln, int64(3)) // f3 f4 fl
	})
}

func TestLiveSortedSets(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		z := k("z")
		n, err := c.ZAdd(ctx, z, hotkv.Z{Score: 1, Member: "a"}, hotkv.Z{Score: 2.5, Member: "b"}, hotkv.Z{Score: 3, Member: "c"}).Result()
		ck(t, err, "ZADD")
		eq(t, "ZADD", n, int64(3))
		n, _ = c.ZAddArgs(ctx, z, hotkv.ZAddArgs{NX: true}, hotkv.Z{Score: 9, Member: "a"}, hotkv.Z{Score: 4, Member: "d"}).Result()
		eq(t, "ZADD NX", n, int64(1))
		n, _ = c.ZAddArgs(ctx, z, hotkv.ZAddArgs{XX: true, CH: true}, hotkv.Z{Score: 5, Member: "d"}, hotkv.Z{Score: 1, Member: "nonmember"}).Result()
		eq(t, "ZADD XX CH", n, int64(1))
		n, _ = c.ZAddArgs(ctx, z, hotkv.ZAddArgs{GT: true, CH: true}, hotkv.Z{Score: 1, Member: "d"}).Result()
		eq(t, "ZADD GT (lower score ignored)", n, int64(0))
		n, _ = c.ZAddArgs(ctx, z, hotkv.ZAddArgs{LT: true, CH: true}, hotkv.Z{Score: 4, Member: "d"}).Result()
		eq(t, "ZADD LT (lower score applied)", n, int64(1))
		s, err := c.ZScore(ctx, z, "b").Result()
		ck(t, err, "ZSCORE")
		eq(t, "ZSCORE", s, 2.5)
		_, err = c.ZScore(ctx, z, "none").Result()
		isNil(t, "ZSCORE missing", err)
		r, err := c.ZRank(ctx, z, "a").Result()
		ck(t, err, "ZRANK")
		eq(t, "ZRANK", r, int64(0))
		_, err = c.ZRank(ctx, z, "none").Result()
		isNil(t, "ZRANK missing member", err)
		rr, _ := c.ZRevRank(ctx, z, "a").Result()
		eq(t, "ZREVRANK", rr, int64(3))
		_, err = c.ZRevRank(ctx, z, "none").Result()
		isNil(t, "ZREVRANK missing member", err)
		cd, _ := c.ZCard(ctx, z).Result()
		eq(t, "ZCARD", cd, int64(4))
		rng, _ := c.ZRange(ctx, z, 0, -1).Result()
		eq(t, "ZRANGE", rng, []string{"a", "b", "c", "d"})
		ws, err := c.ZRangeWithScores(ctx, z, 0, 1).Result()
		ck(t, err, "ZRANGE WITHSCORES")
		eq(t, "ZRANGE WITHSCORES", ws, []hotkv.Z{{Score: 1, Member: "a"}, {Score: 2.5, Member: "b"}})
		rev, _ := c.ZRevRange(ctx, z, 0, 1).Result()
		eq(t, "ZREVRANGE", rev, []string{"d", "c"})
		bs, _ := c.ZRangeByScore(ctx, z, "(1", "3").Result()
		eq(t, "ZRANGEBYSCORE (1 3", bs, []string{"b", "c"})
		bs, _ = c.ZRangeByScore(ctx, z, "-inf", "+inf").Result()
		eq(t, "ZRANGEBYSCORE -inf +inf", bs, []string{"a", "b", "c", "d"})
		rbs, _ := c.ZRevRangeByScore(ctx, z, "3", "2").Result()
		eq(t, "ZREVRANGEBYSCORE", rbs, []string{"c", "b"})
		cnt, _ := c.ZCount(ctx, z, "2", "4").Result()
		eq(t, "ZCOUNT", cnt, int64(3))
		inc, err := c.ZIncrBy(ctx, z, 1e-5, "a").Result()
		ck(t, err, "ZINCRBY")
		near(t, "ZINCRBY tiny", inc, 1.00001, 1e-9)
		inc, _ = c.ZIncrBy(ctx, z, -1.00001, "a").Result()
		near(t, "ZINCRBY negative", inc, 0, 1e-9)

		// infinities as scores and bounds
		ck(t, c.ZAdd(ctx, z, hotkv.Z{Score: inf(1), Member: "top"}, hotkv.Z{Score: inf(-1), Member: "bottom"}).Err(), "ZADD inf")
		rng, _ = c.ZRange(ctx, z, 0, -1).Result()
		eq(t, "ZRANGE with infinite scores", rng, []string{"bottom", "a", "b", "c", "d", "top"})
		sc, _ := c.ZScore(ctx, z, "top").Result()
		eq(t, "ZSCORE +inf", sc, inf(1))
		sc, _ = c.ZScore(ctx, z, "bottom").Result()
		eq(t, "ZSCORE -inf", sc, inf(-1))
		ck(t, c.ZRem(ctx, z, "top", "bottom").Err(), "ZREM inf")

		// pops and random
		pmin, _ := c.ZPopMin(ctx, z).Result()
		eq(t, "ZPOPMIN", pmin, []hotkv.Z{{Score: 0, Member: "a"}})
		pmax, _ := c.ZPopMax(ctx, z, 2).Result()
		eq(t, "ZPOPMAX 2", pmax, []hotkv.Z{{Score: 4, Member: "d"}, {Score: 3, Member: "c"}})
		rm, err := c.ZRandMember(ctx, z).Result()
		ck(t, err, "ZRANDMEMBER")
		eq(t, "ZRANDMEMBER (only b left)", rm, "b")
		rmn, _ := c.ZRandMemberN(ctx, z, 1).Result()
		eq(t, "ZRANDMEMBER 1", rmn, []string{"b"})
		rmws, err := c.ZRandMemberWithScores(ctx, z, 1).Result()
		ck(t, err, "ZRANDMEMBER WITHSCORES")
		eq(t, "ZRANDMEMBER WITHSCORES", rmws, []hotkv.Z{{Score: 2.5, Member: "b"}})
		rem, _ := c.ZRem(ctx, z, "b", "none").Result()
		eq(t, "ZREM", rem, int64(1))

		// set algebra
		x, y, dst := k("zx"), k("zy"), k("zdst")
		ck(t, c.ZAdd(ctx, x, hotkv.Z{Score: 1, Member: "m1"}, hotkv.Z{Score: 2, Member: "m2"}).Err(), "ZADD x")
		ck(t, c.ZAdd(ctx, y, hotkv.Z{Score: 10, Member: "m2"}, hotkv.Z{Score: 20, Member: "m3"}).Err(), "ZADD y")
		n, _ = c.ZUnionStore(ctx, dst, &hotkv.ZStore{Keys: []string{x, y}}).Result()
		eq(t, "ZUNIONSTORE", n, int64(3))
		ws, _ = c.ZRangeWithScores(ctx, dst, 0, -1).Result()
		eq(t, "ZUNIONSTORE result", ws, []hotkv.Z{{Score: 1, Member: "m1"}, {Score: 12, Member: "m2"}, {Score: 20, Member: "m3"}})
		n, _ = c.ZUnionStore(ctx, dst, &hotkv.ZStore{Keys: []string{x, y}, Weights: []float64{2, 0.5}, Aggregate: "MAX"}).Result()
		eq(t, "ZUNIONSTORE weights", n, int64(3))
		ws, _ = c.ZRangeWithScores(ctx, dst, 0, -1).Result()
		eq(t, "ZUNIONSTORE weights result", ws, []hotkv.Z{{Score: 2, Member: "m1"}, {Score: 5, Member: "m2"}, {Score: 10, Member: "m3"}})
		n, _ = c.ZInterStore(ctx, dst, &hotkv.ZStore{Keys: []string{x, y}}).Result()
		eq(t, "ZINTERSTORE", n, int64(1))
		ws, _ = c.ZRangeWithScores(ctx, dst, 0, -1).Result()
		eq(t, "ZINTERSTORE result", ws, []hotkv.Z{{Score: 12, Member: "m2"}})

		var seen []string
		var cur uint64
		for i := 0; i < 100; i++ {
			nc, ks, err := c.ZScan(ctx, x, cur, "", 100).Result()
			ck(t, err, "ZSCAN")
			seen = append(seen, ks...)
			if cur = nc; cur == 0 {
				break
			}
		}
		eq(t, "ZSCAN member/score pairs", seen, []string{"m1", "1", "m2", "2"})
	})
}

func TestLiveStreams(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		s := k("stream")
		id1, err := c.XAdd(ctx, s, "1000-1", "f", "v1", "n", 1).Result()
		ck(t, err, "XADD")
		eq(t, "XADD explicit id", id1, "1000-1")
		id2, _ := c.XAdd(ctx, s, "1000-2", "f", "v2").Result()
		auto, err := c.XAdd(ctx, s, "*", "f", "v3").Result()
		ck(t, err, "XADD *")
		if auto == "" || auto == id1 {
			t.Errorf("XADD * id = %q", auto)
		}
		n, _ := c.XLen(ctx, s).Result()
		eq(t, "XLEN", n, int64(3))
		es, err := c.XRange(ctx, s, "-", "+", 0).Result()
		ck(t, err, "XRANGE")
		if len(es) != 3 || es[0].ID != "1000-1" || es[0].Fields["f"] != "v1" || es[0].Fields["n"] != "1" || es[1].ID != id2 || es[2].Fields["f"] != "v3" {
			t.Errorf("XRANGE = %+v", es)
		}
		es, _ = c.XRange(ctx, s, "-", "+", 2).Result()
		eq(t, "XRANGE COUNT 2 length", len(es), 2)
		rv, err := c.XRevRange(ctx, s, "+", "-", 1).Result()
		ck(t, err, "XREVRANGE")
		if len(rv) != 1 || rv[0].ID != auto {
			t.Errorf("XREVRANGE = %+v", rv)
		}
		xr, err := c.XRead(ctx, 0, map[string]string{s: "1000-1"}).Result()
		ck(t, err, "XREAD")
		if len(xr) != 1 || xr[0].Stream != s || len(xr[0].Entries) != 2 || xr[0].Entries[0].ID != id2 {
			t.Errorf("XREAD = %+v", xr)
		}
		info, err := c.XInfoStream(ctx, s).Result()
		ck(t, err, "XINFO STREAM")
		if l, ok := info["length"]; !ok || l.Int != 3 {
			t.Errorf("XINFO STREAM length = %+v", info["length"])
		}
		d, _ := c.XDel(ctx, s, id1, "9-9").Result()
		eq(t, "XDEL", d, int64(1))
		tr, _ := c.XTrimMaxLen(ctx, s, 1, true).Result()
		eq(t, "XTRIM MAXLEN", tr, int64(1))
		n, _ = c.XLen(ctx, s).Result()
		eq(t, "XLEN after trim", n, int64(1))
	})
}

func TestLiveBitmapsAndHLL(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		b := k("bits")
		old, err := c.SetBit(ctx, b, 7, 1).Result()
		ck(t, err, "SETBIT")
		eq(t, "SETBIT old value", old, int64(0))
		c.SetBit(ctx, b, 0, 1)
		c.SetBit(ctx, b, 15, 1)
		g, _ := c.GetBit(ctx, b, 7).Result()
		eq(t, "GETBIT set", g, int64(1))
		g, _ = c.GetBit(ctx, b, 8).Result()
		eq(t, "GETBIT unset", g, int64(0))
		cnt, _ := c.BitCount(ctx, b, nil).Result()
		eq(t, "BITCOUNT", cnt, int64(3))
		cnt, _ = c.BitCount(ctx, b, &hotkv.BitCount{Start: 0, End: 0}).Result()
		eq(t, "BITCOUNT 0 0", cnt, int64(2))
		pos, _ := c.BitPos(ctx, b, 1).Result()
		eq(t, "BITPOS 1", pos, int64(0))
		pos, _ = c.BitPos(ctx, b, 0).Result()
		eq(t, "BITPOS 0", pos, int64(1))

		x, y := k("bx"), k("by")
		ck(t, c.Set(ctx, x, "\xff\x0f", 0).Err(), "SET bx")
		ck(t, c.Set(ctx, y, "\x0f\xff", 0).Err(), "SET by")
		ln, _ := c.BitOpAnd(ctx, k("band"), x, y).Result()
		eq(t, "BITOP AND length", ln, int64(2))
		v, _ := c.Get(ctx, k("band")).Result()
		eq(t, "BITOP AND", v, "\x0f\x0f")
		c.BitOpOr(ctx, k("bor"), x, y)
		v, _ = c.Get(ctx, k("bor")).Result()
		eq(t, "BITOP OR", v, "\xff\xff")
		c.BitOpXor(ctx, k("bxor"), x, y)
		v, _ = c.Get(ctx, k("bxor")).Result()
		eq(t, "BITOP XOR", v, "\xf0\xf0")
		c.BitOpNot(ctx, k("bnot"), x)
		v, _ = c.Get(ctx, k("bnot")).Result()
		eq(t, "BITOP NOT", v, "\x00\xf0")

		h1, h2 := k("hll1"), k("hll2")
		ch, err := c.PFAdd(ctx, h1, "a", "b", "c", "a").Result()
		ck(t, err, "PFADD")
		eq(t, "PFADD changed", ch, int64(1))
		ch, _ = c.PFAdd(ctx, h1, "a").Result()
		eq(t, "PFADD unchanged", ch, int64(0))
		c.PFAdd(ctx, h2, "c", "d")
		pc, _ := c.PFCount(ctx, h1).Result()
		eq(t, "PFCOUNT", pc, int64(3))
		pc, _ = c.PFCount(ctx, h1, h2).Result()
		eq(t, "PFCOUNT union", pc, int64(4))
		ck(t, c.PFMerge(ctx, k("hllm"), h1, h2).Err(), "PFMERGE")
		pc, _ = c.PFCount(ctx, k("hllm")).Result()
		eq(t, "PFCOUNT merged", pc, int64(4))
	})
}

func inf(sign int) float64 {
	if sign >= 0 {
		return posInf
	}
	return negInf
}
