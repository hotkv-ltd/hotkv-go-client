// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"testing"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

// These tests drive commands that have no typed method through the generic
// Do method, to show that it works for any command and reply shape.

func TestLiveDoProbabilistic(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		bf := k("bf")
		s, err := c.Do(ctx, "BF.RESERVE", bf, 0.01, 100).Text()
		ck(t, err, "BF.RESERVE")
		eq(t, "BF.RESERVE", s, "OK")
		n, _ := c.Do(ctx, "BF.ADD", bf, "a").Int64()
		eq(t, "BF.ADD new", n, int64(1))
		n, _ = c.Do(ctx, "BF.ADD", bf, "a").Int64()
		eq(t, "BF.ADD again", n, int64(0))
		b, _ := c.Do(ctx, "BF.EXISTS", bf, "a").Bool()
		eq(t, "BF.EXISTS", b, true)
		b, _ = c.Do(ctx, "BF.EXISTS", bf, "zzz").Bool()
		eq(t, "BF.EXISTS missing", b, false)
		madd := c.Do(ctx, "BF.MADD", bf, "b", "c")
		ck(t, madd.Err(), "BF.MADD")
		eq(t, "BF.MADD", flat(madd.Val()), []string{"1", "1"})
		eq(t, "BF.MEXISTS", flat(c.Do(ctx, "BF.MEXISTS", bf, "a", "zzz", "c").Val()), []string{"1", "0", "1"})
		n, _ = c.Do(ctx, "BF.CARD", bf).Int64()
		eq(t, "BF.CARD", n, int64(3))
		fl := flat(c.Do(ctx, "BF.INFO", bf).Val())
		if v, _ := field(fl, "Capacity"); v != "100" {
			t.Errorf("BF.INFO = %v", fl)
		}
		ins := c.Do(ctx, "BF.INSERT", k("bf2"), "CAPACITY", 100, "ERROR", 0.01, "ITEMS", "q", "r")
		ck(t, ins.Err(), "BF.INSERT")
		eq(t, "BF.INSERT", flat(ins.Val()), []string{"1", "1"})

		cms := k("cms")
		s, _ = c.Do(ctx, "CMS.INITBYDIM", cms, 100, 5).Text()
		eq(t, "CMS.INITBYDIM", s, "OK")
		incr := c.Do(ctx, "CMS.INCRBY", cms, "a", 3, "b", 2)
		ck(t, incr.Err(), "CMS.INCRBY")
		eq(t, "CMS.INCRBY", flat(incr.Val()), []string{"3", "2"})
		eq(t, "CMS.QUERY", flat(c.Do(ctx, "CMS.QUERY", cms, "a", "b", "z").Val()), []string{"3", "2", "0"})
		// probabilities are sent as plain decimals, never as 1e-05
		s, err = c.Do(ctx, "CMS.INITBYPROB", k("cms2"), 1e-5, 0.01).Text()
		ck(t, err, "CMS.INITBYPROB with a tiny probability")
		eq(t, "CMS.INITBYPROB", s, "OK")
		if v, _ := field(flat(c.Do(ctx, "CMS.INFO", cms).Val()), "width"); v != "100" {
			t.Errorf("CMS.INFO = %v", flat(c.Do(ctx, "CMS.INFO", cms).Val()))
		}

		tk := k("topk")
		s, _ = c.Do(ctx, "TOPK.RESERVE", tk, 2).Text()
		eq(t, "TOPK.RESERVE", s, "OK")
		ck(t, c.Do(ctx, "TOPK.ADD", tk, "a", "a", "a", "b", "c").Err(), "TOPK.ADD")
		ls, _ := c.Do(ctx, "TOPK.LIST", tk).StringSlice()
		if len(ls) != 2 || ls[0] != "a" {
			t.Errorf("TOPK.LIST = %v", ls)
		}
		eq(t, "TOPK.QUERY", flat(c.Do(ctx, "TOPK.QUERY", tk, "a", "zzz").Val()), []string{"1", "0"})
		eq(t, "TOPK.COUNT", flat(c.Do(ctx, "TOPK.COUNT", tk, "a").Val()), []string{"3"})
	})
}

func TestLiveDoNewerHashAndStringCommands(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		h := k("h")
		ck(t, c.HSet(ctx, h, "a", 1, "b", 2, "c", 3).Err(), "HSET")
		got, err := c.Do(ctx, "HGETDEL", h, "FIELDS", 1, "a").Slice()
		ck(t, err, "HGETDEL")
		eq(t, "HGETDEL", got, []interface{}{"1"})
		ex, _ := c.HExists(ctx, h, "a").Result()
		eq(t, "field deleted by HGETDEL", ex, false)
		got, err = c.Do(ctx, "HGETEX", h, "EX", 100, "FIELDS", 1, "b").Slice()
		ck(t, err, "HGETEX")
		eq(t, "HGETEX", got, []interface{}{"2"})
		ttl, _ := c.Do(ctx, "HTTL", h, "FIELDS", 1, "b").Slice()
		if len(ttl) != 1 || ttl[0].(int64) <= 90 || ttl[0].(int64) > 100 {
			t.Errorf("HTTL after HGETEX EX 100 = %v", ttl)
		}
		n, err := c.Do(ctx, "HSETEX", h, "EX", 100, "FIELDS", 1, "d", 4).Int64()
		ck(t, err, "HSETEX")
		eq(t, "HSETEX", n, int64(1))
		d, _ := c.HGet(ctx, h, "d").Result()
		eq(t, "HSETEX value", d, "4")

		m1, m2 := k("mk1"), k("mk2")
		n, err = c.Do(ctx, "MSETEX", 2, m1, "v1", m2, "v2", "EX", 100).Int64()
		ck(t, err, "MSETEX")
		eq(t, "MSETEX", n, int64(1))
		t1, _ := c.TTL(ctx, m1).Result()
		if t1 <= 0 || t1 > 100*1e9 {
			t.Errorf("TTL after MSETEX EX 100 = %v", t1)
		}
		ck(t, c.Set(ctx, k("dx"), "hello", 0).Err(), "SET dx")
		n, err = c.Do(ctx, "DELEX", k("dx"), "IFEQ", "nope").Int64()
		ck(t, err, "DELEX")
		eq(t, "DELEX with the wrong value", n, int64(0))
		n, _ = c.Do(ctx, "DELEX", k("dx"), "IFEQ", "hello").Int64()
		eq(t, "DELEX with the right value", n, int64(1))
		_, err = c.Get(ctx, k("dx")).Result()
		isNil(t, "GET after DELEX", err)
	})
}

func TestLiveDoGeoListScript(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		g := k("geo")
		n, err := c.Do(ctx, "GEOADD", g, 13.361389, 38.115556, "Palermo", 15.087269, 37.502669, "Catania").Int64()
		ck(t, err, "GEOADD")
		eq(t, "GEOADD", n, int64(2))
		dist, err := c.Do(ctx, "GEODIST", g, "Palermo", "Catania", "km").Float64()
		ck(t, err, "GEODIST")
		near(t, "GEODIST", dist, 166.2742, 0.01)
		res, err := c.Do(ctx, "GEOSEARCH", g, "FROMLONLAT", 15, 37, "BYRADIUS", 200, "km", "ASC").StringSlice()
		ck(t, err, "GEOSEARCH")
		eq(t, "GEOSEARCH", res, []string{"Catania", "Palermo"})

		l := k("lm")
		c.RPush(ctx, l, "a", "b", "c")
		mv, err := c.Do(ctx, "LMOVE", l, k("lm2"), "LEFT", "RIGHT").Text()
		ck(t, err, "LMOVE")
		eq(t, "LMOVE", mv, "a")
		_, err = c.Do(ctx, "LMOVE", k("empty"), k("empty2"), "LEFT", "RIGHT").Result()
		isNil(t, "LMOVE of a missing list", err)

		v, err := c.Do(ctx, "EVAL", "return {KEYS[1], ARGV[1], ARGV[2]}", 1, k("ek"), "a1", 2.5).StringSlice()
		ck(t, err, "EVAL")
		eq(t, "EVAL", v, []string{k("ek"), "a1", "2.5"})
		iv, _ := c.Do(ctx, "EVAL", "return 42", 0).Int64()
		eq(t, "EVAL integer", iv, int64(42))
		bin, _ := c.Do(ctx, "EVAL", "return ARGV[1]", 0, []byte{0, 1, 2, 255}).Text()
		eq(t, "EVAL binary argument", bin, "\x00\x01\x02\xff")
		sha, err := c.Do(ctx, "SCRIPT", "LOAD", "return 7").Text()
		ck(t, err, "SCRIPT LOAD")
		iv, _ = c.Do(ctx, "EVALSHA", sha, 0).Int64()
		eq(t, "EVALSHA", iv, int64(7))
	})
}

// TestLiveDoClientTracking uses a one-connection pool: tracking is a
// property of the connection.
func TestLiveDoClientTracking(t *testing.T) {
	for _, proto := range []int{2, 3} {
		opts := liveOptions(t)
		opts.PoolSize = 1
		opts.Protocol = proto
		c, err := hotkv.NewClient(opts)
		ck(t, err, "NewClient")
		ctx := context.Background()
		s, err := c.Do(ctx, "CLIENT", "TRACKING", "on", "OPTIN").Text()
		if err != nil {
			// RESP2 clients without a redirect target cannot enable tracking
			t.Logf("RESP%d: CLIENT TRACKING on OPTIN: %v", proto, err)
			c.Close()
			continue
		}
		eq(t, "CLIENT TRACKING on", s, "OK")
		ck(t, c.Do(ctx, "CLIENT", "CACHING", "yes").Err(), "CLIENT CACHING")
		ti := c.Do(ctx, "CLIENT", "TRACKINGINFO")
		ck(t, ti.Err(), "CLIENT TRACKINGINFO")
		fl := flat(ti.Val())
		if f, _ := field(fl, "flags"); f == "" || f == "off" {
			t.Errorf("RESP%d: CLIENT TRACKINGINFO = %v, want tracking on", proto, fl)
		}
		redir, err := c.Do(ctx, "CLIENT", "GETREDIR").Int64()
		ck(t, err, "CLIENT GETREDIR")
		if redir < -1 {
			t.Errorf("CLIENT GETREDIR = %d", redir)
		}
		ck(t, c.Do(ctx, "CLIENT", "TRACKING", "off").Err(), "CLIENT TRACKING off")
		c.Close()
	}
}

// FEATURE.SETAT is an enterprise command with no typed method.
func TestLiveDoFeatureSetAt(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		g := k("fsa")
		ck(t, c.FeatureCreate(ctx, g, "setat", map[string]string{"score": "int64"}).Err(), "FEATURE.CREATE")
		defer c.FeatureDrop(ctx, g)
		s, err := c.Do(ctx, "FEATURE.SETAT", g, "e1", 1700000000000, "score", 5).Text()
		ck(t, err, "FEATURE.SETAT")
		eq(t, "FEATURE.SETAT", s, "OK")
		got, err := c.FeatureGet(ctx, g, "e1").Result()
		ck(t, err, "FEATURE.GET")
		fl := flat(got...)
		if v, _ := field(fl, "score"); v != "5" || !has(fl, "1700000000000") {
			t.Errorf("FEATURE.GET after SETAT = %v, want score 5 stamped 1700000000000", fl)
		}
	})
}
