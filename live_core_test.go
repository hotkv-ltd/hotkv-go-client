// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

// TestLiveStrings calls every typed string command and checks the reply.
func TestLiveStrings(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()

		// SET / GET with every expiration form
		ck(t, c.Set(ctx, k("s1"), "v1", 0).Err(), "SET")
		v, err := c.Get(ctx, k("s1")).Result()
		ck(t, err, "GET")
		eq(t, "GET", v, "v1")
		_, err = c.Get(ctx, k("missing")).Result()
		isNil(t, "GET missing", err)

		ck(t, c.Set(ctx, k("s-ex"), "x", 100*time.Second).Err(), "SET EX")
		ttl, _ := c.TTL(ctx, k("s-ex")).Result()
		if ttl <= 0 || ttl > 100*time.Second {
			t.Errorf("TTL after SET EX 100 = %v", ttl)
		}
		ck(t, c.Set(ctx, k("s-px"), "x", 1500*time.Millisecond).Err(), "SET PX")
		pttl, _ := c.PTTL(ctx, k("s-px")).Result()
		if pttl <= 0 || pttl > 1500*time.Millisecond {
			t.Errorf("PTTL after SET PX 1500 = %v", pttl)
		}

		// SETNX / SETXX report whether the write happened
		ok, err := c.SetNX(ctx, k("nx"), "a", 0).Result()
		ck(t, err, "SETNX")
		eq(t, "SetNX on a new key", ok, true)
		ok, err = c.SetNX(ctx, k("nx"), "b", 0).Result()
		ck(t, err, "SETNX 2")
		eq(t, "SetNX on an existing key", ok, false)
		v, _ = c.Get(ctx, k("nx")).Result()
		eq(t, "value after failed SETNX", v, "a")
		ok, err = c.SetXX(ctx, k("xx-missing"), "a", 0).Result()
		ck(t, err, "SETXX missing")
		eq(t, "SetXX on a missing key", ok, false)
		ok, err = c.SetXX(ctx, k("nx"), "c", 0).Result()
		ck(t, err, "SETXX existing")
		eq(t, "SetXX on an existing key", ok, true)
		v, _ = c.Get(ctx, k("nx")).Result()
		eq(t, "value after SETXX", v, "c")
		ok, err = c.SetNX(ctx, k("nx-ttl"), "t", time.Minute).Result()
		ck(t, err, "SETNX EX")
		eq(t, "SetNX with expiry", ok, true)

		// SETEX / PSETEX / GETEX / GETDEL / GETSET
		ck(t, c.SetEx(ctx, k("setex"), "e", 50*time.Second).Err(), "SETEX")
		ttl, _ = c.TTL(ctx, k("setex")).Result()
		if ttl <= 0 || ttl > 50*time.Second {
			t.Errorf("TTL after SETEX = %v", ttl)
		}
		ck(t, c.PSetEx(ctx, k("psetex"), "p", 40*time.Second).Err(), "PSETEX")
		pttl, _ = c.PTTL(ctx, k("psetex")).Result()
		if pttl <= 30*time.Second || pttl > 40*time.Second {
			t.Errorf("PTTL after PSETEX = %v", pttl)
		}
		v, err = c.GetEx(ctx, k("setex"), 0).Result() // 0 = PERSIST
		ck(t, err, "GETEX persist")
		eq(t, "GETEX value", v, "e")
		ttl, _ = c.TTL(ctx, k("setex")).Result()
		eq(t, "TTL after GETEX PERSIST", ttl, time.Duration(-1))
		v, _ = c.GetEx(ctx, k("setex"), 20*time.Second).Result()
		ttl, _ = c.TTL(ctx, k("setex")).Result()
		if ttl <= 0 || ttl > 20*time.Second || v != "e" {
			t.Errorf("GETEX EX: value %q ttl %v", v, ttl)
		}
		v, err = c.GetDel(ctx, k("setex")).Result()
		ck(t, err, "GETDEL")
		eq(t, "GETDEL", v, "e")
		_, err = c.Get(ctx, k("setex")).Result()
		isNil(t, "GET after GETDEL", err)
		v, err = c.GetSet(ctx, k("s1"), "v2").Result()
		ck(t, err, "GETSET")
		eq(t, "GETSET old value", v, "v1")

		// ranges and sizes
		ck(t, c.Set(ctx, k("rng"), "Hello World", 0).Err(), "SET rng")
		v, _ = c.GetRange(ctx, k("rng"), 0, 4).Result()
		eq(t, "GETRANGE 0 4", v, "Hello")
		v, _ = c.GetRange(ctx, k("rng"), -5, -1).Result()
		eq(t, "GETRANGE -5 -1", v, "World")
		n, err := c.SetRange(ctx, k("rng"), 6, "Redis").Result()
		ck(t, err, "SETRANGE")
		eq(t, "SETRANGE length", n, int64(11))
		v, _ = c.Get(ctx, k("rng")).Result()
		eq(t, "after SETRANGE", v, "Hello Redis")
		n, _ = c.StrLen(ctx, k("rng")).Result()
		eq(t, "STRLEN", n, int64(11))
		n, _ = c.Append(ctx, k("rng"), "!!").Result()
		eq(t, "APPEND length", n, int64(13))

		// counters
		n, _ = c.Incr(ctx, k("cnt")).Result()
		eq(t, "INCR", n, int64(1))
		n, _ = c.IncrBy(ctx, k("cnt"), 10).Result()
		eq(t, "INCRBY", n, int64(11))
		n, _ = c.Decr(ctx, k("cnt")).Result()
		eq(t, "DECR", n, int64(10))
		n, _ = c.DecrBy(ctx, k("cnt"), 4).Result()
		eq(t, "DECRBY", n, int64(6))
		f, err := c.IncrByFloat(ctx, k("fl"), 1.5).Result()
		ck(t, err, "INCRBYFLOAT")
		near(t, "INCRBYFLOAT", f, 1.5, 1e-9)
		f, _ = c.IncrByFloat(ctx, k("fl"), 1e-5).Result()
		near(t, "INCRBYFLOAT tiny", f, 1.50001, 1e-9)
		f, _ = c.IncrByFloat(ctx, k("fl"), -0.5).Result()
		near(t, "INCRBYFLOAT negative", f, 1.00001, 1e-9)
		ck(t, c.Set(ctx, k("notnum"), "abc", 0).Err(), "SET notnum")
		if err := c.Incr(ctx, k("notnum")).Err(); err == nil {
			t.Error("INCR on a non-number must fail")
		}

		// multi-key
		ck(t, c.MSet(ctx, k("m1"), "a", k("m2"), 2, k("m3"), []byte{0, 1, 2}).Err(), "MSET")
		vals, err := c.MGet(ctx, k("m1"), k("m2"), k("m-none"), k("m3")).Result()
		ck(t, err, "MGET")
		if len(vals) != 4 || vals[0].Str != "a" || vals[1].Str != "2" || !vals[2].IsNil() || vals[3].Str != "\x00\x01\x02" {
			t.Errorf("MGET = %+v", vals)
		}
		ok, err = c.MSetNX(ctx, k("m1"), "z", k("m9"), "z").Result()
		ck(t, err, "MSETNX existing")
		eq(t, "MSETNX with an existing key", ok, false)
		ok, err = c.MSetNX(ctx, k("n1"), "1", k("n2"), "2").Result()
		ck(t, err, "MSETNX new")
		eq(t, "MSETNX new keys", ok, true)

		// binary safety
		bin := string([]byte{0, 1, 2, 255, '\r', '\n', 'x', 0})
		ck(t, c.Set(ctx, k("bin"), bin, 0).Err(), "SET bin")
		v, _ = c.Get(ctx, k("bin")).Result()
		eq(t, "binary value round trip", v, bin)
		big := strings.Repeat("0123456789abcdef", 64*1024) // 1 MiB
		ck(t, c.Set(ctx, k("big"), big, 10*time.Minute).Err(), "SET 1MiB")
		v, _ = c.Get(ctx, k("big")).Result()
		eq(t, "1 MiB value round trip", v == big, true)
	})
}

// TestLiveKeys calls every typed generic key command.
func TestLiveKeys(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		for _, s := range []string{"a", "b", "c"} {
			ck(t, c.Set(ctx, k("k-"+s), s, 0).Err(), "SET")
		}
		n, _ := c.Exists(ctx, k("k-a"), k("k-b"), k("k-none")).Result()
		eq(t, "EXISTS", n, int64(2))
		n, _ = c.Touch(ctx, k("k-a"), k("k-none")).Result()
		eq(t, "TOUCH", n, int64(1))

		tp, err := c.Type(ctx, k("k-a")).Result()
		ck(t, err, "TYPE")
		eq(t, "TYPE string", tp, "string")
		tp, _ = c.Type(ctx, k("k-none")).Result()
		eq(t, "TYPE missing", tp, "none")

		keys, err := c.Keys(ctx, k("k-*")).Result()
		ck(t, err, "KEYS")
		eqUnordered(t, "KEYS", keys, []string{k("k-a"), k("k-b"), k("k-c")})

		var scanned []string
		var cur uint64
		for i := 0; i < 1000; i++ {
			nc, ks, err := c.Scan(ctx, cur, k("k-*"), 100).Result()
			ck(t, err, "SCAN")
			scanned = append(scanned, ks...)
			cur = nc
			if cur == 0 {
				break
			}
		}
		eqUnordered(t, "SCAN MATCH", dedupe(scanned), []string{k("k-a"), k("k-b"), k("k-c")})

		rk, err := c.RandomKey(ctx).Result()
		ck(t, err, "RANDOMKEY")
		if rk == "" {
			t.Error("RANDOMKEY empty on a non-empty db")
		}

		// rename / copy
		ck(t, c.Rename(ctx, k("k-a"), k("k-a2")).Err(), "RENAME")
		_, err = c.Get(ctx, k("k-a")).Result()
		isNil(t, "GET renamed-away key", err)
		ok, err := c.RenameNX(ctx, k("k-a2"), k("k-b")).Result()
		ck(t, err, "RENAMENX")
		eq(t, "RENAMENX onto an existing key", ok, false)
		ok, err = c.RenameNX(ctx, k("k-a2"), k("k-a3")).Result()
		ck(t, err, "RENAMENX free")
		eq(t, "RENAMENX to a free name", ok, true)
		ok, err = c.Copy(ctx, k("k-a3"), k("k-copy"), false).Result()
		ck(t, err, "COPY")
		eq(t, "COPY", ok, true)
		ok, _ = c.Copy(ctx, k("k-a3"), k("k-copy"), false).Result()
		eq(t, "COPY onto existing without REPLACE", ok, false)
		ok, _ = c.Copy(ctx, k("k-b"), k("k-copy"), true).Result()
		eq(t, "COPY REPLACE", ok, true)
		v, _ := c.Get(ctx, k("k-copy")).Result()
		eq(t, "value after COPY REPLACE", v, "b")

		// expiry family
		eq2 := func(what string, got, want interface{}) { eq(t, what, got, want) }
		ok, _ = c.Expire(ctx, k("k-b"), 100*time.Second).Result()
		eq2("EXPIRE", ok, true)
		ok, _ = c.Expire(ctx, k("k-none"), time.Second).Result()
		eq2("EXPIRE on a missing key", ok, false)
		ttl, _ := c.TTL(ctx, k("k-b")).Result()
		if ttl <= 0 || ttl > 100*time.Second {
			t.Errorf("TTL = %v", ttl)
		}
		ttlMissing, _ := c.TTL(ctx, k("k-none")).Result()
		eq2("TTL missing", ttlMissing, time.Duration(-2))
		ok, _ = c.Persist(ctx, k("k-b")).Result()
		eq2("PERSIST", ok, true)
		ttl, _ = c.TTL(ctx, k("k-b")).Result()
		eq2("TTL after PERSIST", ttl, time.Duration(-1))
		ok, _ = c.PExpire(ctx, k("k-b"), 90*time.Second).Result()
		eq2("PEXPIRE", ok, true)
		pttl, _ := c.PTTL(ctx, k("k-b")).Result()
		if pttl <= 80*time.Second || pttl > 90*time.Second {
			t.Errorf("PTTL = %v", pttl)
		}
		at := time.Now().Add(2 * time.Hour)
		ok, _ = c.ExpireAt(ctx, k("k-b"), at).Result()
		eq2("EXPIREAT", ok, true)
		et, err := c.ExpireTime(ctx, k("k-b")).Result()
		ck(t, err, "EXPIRETIME")
		eq2("EXPIRETIME", int64(et/time.Second), at.Unix())
		ok, _ = c.PExpireAt(ctx, k("k-b"), at.Add(time.Hour)).Result()
		eq2("PEXPIREAT", ok, true)
		pet, err := c.PExpireTime(ctx, k("k-b")).Result()
		ck(t, err, "PEXPIRETIME")
		eq2("PEXPIRETIME", int64(pet/time.Millisecond), at.Add(time.Hour).UnixMilli())

		// del / unlink
		n, _ = c.Del(ctx, k("k-b"), k("k-none")).Result()
		eq2("DEL", n, int64(1))
		n, _ = c.Unlink(ctx, k("k-c"), k("k-copy")).Result()
		eq2("UNLINK", n, int64(2))
		n, _ = c.Exists(ctx, k("k-c"), k("k-copy"), k("k-b")).Result()
		eq2("EXISTS after DEL/UNLINK", n, int64(0))

		// introspection
		ck(t, c.Set(ctx, k("enc"), "12345", 0).Err(), "SET enc")
		enc, err := c.ObjectEncoding(ctx, k("enc")).Result()
		ck(t, err, "OBJECT ENCODING")
		eq2("OBJECT ENCODING of an integer string", enc, "int")
		mu, err := c.MemoryUsage(ctx, k("enc")).Result()
		ck(t, err, "MEMORY USAGE")
		if mu <= 0 {
			t.Errorf("MEMORY USAGE = %d", mu)
		}

		// SORT
		ck(t, c.RPush(ctx, k("sortme"), 3, 1, 2, 10).Err(), "RPUSH")
		s, err := c.Sort(ctx, k("sortme"), &hotkv.SortOptions{Asc: true}).Result()
		ck(t, err, "SORT")
		eq2("SORT ASC", s, []string{"1", "2", "3", "10"})
		s, _ = c.Sort(ctx, k("sortme"), &hotkv.SortOptions{Desc: true, Count: 2}).Result()
		eq2("SORT DESC LIMIT 0 2", s, []string{"10", "3"})
		s, _ = c.Sort(ctx, k("sortme"), &hotkv.SortOptions{Alpha: true, Asc: true}).Result()
		eq2("SORT ALPHA", s, []string{"1", "10", "2", "3"})
	})
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
