// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

func TestLiveServerCommands(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		pong, err := c.Ping(ctx).Result()
		ck(t, err, "PING")
		eq(t, "PING", pong, "PONG")
		e, err := c.Echo(ctx, "hello\r\nworld").Result()
		ck(t, err, "ECHO")
		eq(t, "ECHO (binary safe)", e, "hello\r\nworld")

		info, err := c.Info(ctx).Result()
		ck(t, err, "INFO")
		if !strings.Contains(info, "# Server") || !strings.Contains(info, "redis_version") && !strings.Contains(info, "hotkv_version") {
			t.Errorf("INFO has no # Server section: %.200q", info)
		}
		info, _ = c.Info(ctx, "keyspace").Result()
		if !strings.Contains(info, "# Keyspace") {
			t.Errorf("INFO keyspace = %.200q", info)
		}

		ts, err := c.Time(ctx).Result()
		ck(t, err, "TIME")
		if len(ts) != 2 {
			t.Fatalf("TIME = %+v", ts)
		}
		secs, _ := strconv.ParseInt(ts[0].Str, 10, 64)
		if d := time.Since(time.Unix(secs, 0)); d < -time.Minute || d > time.Minute {
			t.Errorf("TIME %d is %v away from this host's clock", secs, d)
		}

		_, err = c.LastSave(ctx).Result()
		ck(t, err, "LASTSAVE")
		ck(t, c.Save(ctx).Err(), "SAVE")
		ls, _ := c.LastSave(ctx).Result()
		if time.Since(time.Unix(ls, 0)) > time.Minute {
			t.Errorf("LASTSAVE %d not recent after SAVE", ls)
		}
		bg, err := c.BgSave(ctx).Result()
		ck(t, err, "BGSAVE")
		if !strings.Contains(strings.ToLower(bg), "background saving") && !strings.Contains(strings.ToLower(bg), "scheduled") {
			t.Errorf("BGSAVE = %q", bg)
		}
		waitBgsave(t, c)
		_, err = c.BgRewriteAOF(ctx).Result()
		if err != nil && !strings.Contains(err.Error(), "already") {
			ck(t, err, "BGREWRITEAOF")
		}

		cmds, err := c.Command(ctx).Result()
		ck(t, err, "COMMAND")
		if len(cmds) < 200 {
			t.Errorf("COMMAND listed only %d commands", len(cmds))
		}
		n, err := c.DBSize(ctx).Result()
		ck(t, err, "DBSIZE")
		if n < 0 {
			t.Errorf("DBSIZE = %d", n)
		}

		// CONFIG GET / SET / RESETSTAT, restoring the old value
		cfg, err := c.ConfigGet(ctx, "slowlog-max-len").Result()
		ck(t, err, "CONFIG GET")
		old, okc := cfg["slowlog-max-len"]
		if !okc {
			t.Fatalf("CONFIG GET slowlog-max-len = %v", cfg)
		}
		ck(t, c.ConfigSet(ctx, "slowlog-max-len", "77").Err(), "CONFIG SET")
		cfg, _ = c.ConfigGet(ctx, "slowlog-max-len").Result()
		eq(t, "CONFIG GET after SET", cfg["slowlog-max-len"], "77")
		ck(t, c.ConfigSet(ctx, "slowlog-max-len", old).Err(), "CONFIG SET restore")
		ck(t, c.ConfigResetStat(ctx).Err(), "CONFIG RESETSTAT")
		if err := c.ConfigSet(ctx, "no-such-parameter", "1").Err(); err == nil {
			t.Error("CONFIG SET of an unknown parameter must fail")
		}
	})
}

func waitBgsave(t *testing.T, c *hotkv.Client) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		s, _ := c.Info(ctx, "persistence").Result()
		if strings.Contains(s, "rdb_bgsave_in_progress:0") || !strings.Contains(s, "rdb_bgsave_in_progress") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLiveSlowLog(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		cfg, err := c.ConfigGet(ctx, "slowlog-log-slower-than").Result()
		ck(t, err, "CONFIG GET slowlog")
		old := cfg["slowlog-log-slower-than"]
		defer c.ConfigSet(ctx, "slowlog-log-slower-than", old)
		ck(t, c.ConfigSet(ctx, "slowlog-log-slower-than", "0").Err(), "CONFIG SET slowlog")
		ck(t, c.SlowLogReset(ctx).Err(), "SLOWLOG RESET")
		ck(t, c.Set(ctx, k("slow"), "x", 0).Err(), "SET")
		ln, err := c.SlowLogLen(ctx).Result()
		ck(t, err, "SLOWLOG LEN")
		if ln < 1 {
			t.Errorf("SLOWLOG LEN = %d after logging everything", ln)
		}
		entries, err := c.SlowLogGet(ctx, 5).Result()
		ck(t, err, "SLOWLOG GET")
		if len(entries) == 0 || len(entries[0].Elems) < 4 {
			t.Errorf("SLOWLOG GET = %+v", entries)
		}
		ck(t, c.ConfigSet(ctx, "slowlog-log-slower-than", old).Err(), "restore")
		ck(t, c.SlowLogReset(ctx).Err(), "SLOWLOG RESET")
		ln, _ = c.SlowLogLen(ctx).Result()
		if ln != 0 {
			t.Errorf("SLOWLOG LEN after RESET = %d", ln)
		}
	})
}

// TestLiveConnectionState uses single-connection pools, where connection
// scoped commands (CLIENT SETNAME, SELECT) are meaningful.
func TestLiveConnectionState(t *testing.T) {
	forProtocols(t, func(t *testing.T, _ *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		opts := liveOptions(t)
		opts.PoolSize = 1
		opts.Protocol = 3
		c, err := hotkv.NewClient(opts)
		ck(t, err, "NewClient")
		defer c.Close()

		ck(t, c.ClientSetName(ctx, "gosdk-live").Err(), "CLIENT SETNAME")
		nm, err := c.ClientGetName(ctx).Result()
		ck(t, err, "CLIENT GETNAME")
		eq(t, "CLIENT GETNAME", nm, "gosdk-live")
		id, err := c.ClientID(ctx).Result()
		ck(t, err, "CLIENT ID")
		if id <= 0 {
			t.Errorf("CLIENT ID = %d", id)
		}
		list, err := c.ClientList(ctx).Result()
		ck(t, err, "CLIENT LIST")
		if !strings.Contains(list, "name=gosdk-live") || !strings.Contains(list, "id="+strconv.FormatInt(id, 10)) {
			t.Errorf("CLIENT LIST does not show this connection: %.300q", list)
		}

		// SELECT, SWAPDB, FLUSHDB on private databases 13 and 14
		ck(t, c.Select(ctx, 13).Err(), "SELECT 13")
		ck(t, c.FlushDB(ctx).Err(), "FLUSHDB 13")
		ck(t, c.Set(ctx, k("db13"), "in-13", 0).Err(), "SET in 13")
		n, _ := c.DBSize(ctx).Result()
		eq(t, "DBSIZE of fresh db 13", n, int64(1))
		ck(t, c.Select(ctx, 14).Err(), "SELECT 14")
		ck(t, c.FlushDB(ctx).Err(), "FLUSHDB 14")
		_, err = c.Get(ctx, k("db13")).Result()
		isNil(t, "key of db 13 seen from db 14", err)
		ck(t, c.SwapDB(ctx, 13, 14).Err(), "SWAPDB")
		v, err := c.Get(ctx, k("db13")).Result()
		ck(t, err, "GET after SWAPDB")
		eq(t, "db 14 after SWAPDB holds db 13's data", v, "in-13")
		ck(t, c.FlushDB(ctx).Err(), "FLUSHDB 14 cleanup")
		ck(t, c.Select(ctx, 13).Err(), "SELECT 13")
		ck(t, c.FlushDB(ctx).Err(), "FLUSHDB 13 cleanup")
		ck(t, c.Select(ctx, 0).Err(), "SELECT 0")

		// an Options.DB client lands in that database from its first command
		o2 := *opts
		o2.DB = 12
		c2, _ := hotkv.NewClient(&o2)
		defer c2.Close()
		ck(t, c2.Set(ctx, k("db12"), "x", time.Minute).Err(), "SET in db 12 via Options.DB")
		_, err = c.Get(ctx, k("db12")).Result()
		isNil(t, "db 12 key seen from db 0", err)
		c2.Del(ctx, k("db12"))
	})
}

// TestLiveFlushAll runs FLUSHALL only when HOTKV_TEST_ALLOW_FLUSH=1 (it
// wipes every database of the server).
func TestLiveFlushAll(t *testing.T) {
	if os.Getenv("HOTKV_TEST_ALLOW_FLUSH") != "1" {
		t.Skip("set HOTKV_TEST_ALLOW_FLUSH=1 to run FLUSHALL against HOTKV_TEST_URL")
	}
	c, k := liveClient(t, 3)
	ctx := context.Background()
	ck(t, c.Set(ctx, k("f"), "1", 0).Err(), "SET")
	ck(t, c.FlushAll(ctx).Err(), "FLUSHALL")
	n, _ := c.DBSize(ctx).Result()
	eq(t, "DBSIZE after FLUSHALL", n, int64(0))
}

// TestLiveShutdown stops throwaway servers named by HOTKV_TEST_SHUTDOWN_URLS
// (three comma separated URLs: SHUTDOWN, SHUTDOWN SAVE, SHUTDOWN NOSAVE).
// It never touches HOTKV_TEST_URL.
func TestLiveShutdown(t *testing.T) {
	raw := os.Getenv("HOTKV_TEST_SHUTDOWN_URLS")
	if raw == "" {
		t.Skip("HOTKV_TEST_SHUTDOWN_URLS not set (three throwaway server URLs)")
	}
	urls := strings.Split(raw, ",")
	if len(urls) != 3 {
		t.Fatalf("want 3 URLs, got %d", len(urls))
	}
	modes := []struct {
		name string
		do   func(*hotkv.Client) error
	}{
		{"SHUTDOWN", func(c *hotkv.Client) error { return c.Shutdown(context.Background()).Err() }},
		{"SHUTDOWN SAVE", func(c *hotkv.Client) error { return c.ShutdownSave(context.Background()).Err() }},
		{"SHUTDOWN NOSAVE", func(c *hotkv.Client) error { return c.ShutdownNoSave(context.Background()).Err() }},
	}
	for i, m := range modes {
		opts, err := hotkv.ParseURL(strings.TrimSpace(urls[i]))
		if err != nil {
			t.Fatal(err)
		}
		opts.DialTimeout = time.Second
		opts.MaxRetries = -1
		c, _ := hotkv.NewClient(opts)
		if err := c.Ping(context.Background()).Err(); err != nil {
			t.Fatalf("%s: server %s not reachable first: %v", m.name, urls[i], err)
		}
		if err := m.do(c); err != nil {
			t.Errorf("%s returned %v, want success", m.name, err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := c.Ping(context.Background()).Err(); err == nil {
			t.Errorf("%s: server still answers after shutdown", m.name)
		}
		c.Close()
	}
}

func TestLivePubSub(t *testing.T) {
	forProtocols(t, func(t *testing.T, pub *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		opts := liveOptions(t)
		opts.Protocol = pub.Options().Protocol
		subClient, _ := hotkv.NewClient(opts)
		defer subClient.Close()

		ch1, ch2 := k("ch1"), k("ch2")
		sub := subClient.Subscribe(ctx, ch1, ch2)
		defer sub.Close()
		// confirmations come first
		for i := 0; i < 2; i++ {
			m, err := sub.ReceiveTimeout(ctx)
			ck(t, err, "subscribe confirmation")
			s, ok := m.(*hotkv.Subscription)
			if !ok || s.Kind != "subscribe" {
				t.Fatalf("confirmation %d = %#v", i, m)
			}
		}
		ns, err := pub.Publish(ctx, ch1, "hello").Result()
		ck(t, err, "PUBLISH")
		eq(t, "PUBLISH receivers", ns, int64(1))
		msg, err := sub.ReceiveMessage(ctx)
		ck(t, err, "ReceiveMessage")
		eq(t, "message", *msg, hotkv.Message{Channel: ch1, Payload: "hello"})

		// patterns
		ck(t, sub.PSubscribe(ctx, k("pat")+"*"), "PSUBSCRIBE")
		m, err := sub.ReceiveTimeout(ctx)
		ck(t, err, "psubscribe confirmation")
		if s, ok := m.(*hotkv.Subscription); !ok || s.Kind != "psubscribe" {
			t.Fatalf("confirmation = %#v", m)
		}
		pub.Publish(ctx, k("pat")+"x", "p1")
		msg, err = sub.ReceiveMessage(ctx)
		ck(t, err, "pmessage")
		eq(t, "pmessage", *msg, hotkv.Message{Pattern: k("pat") + "*", Channel: k("pat") + "x", Payload: "p1"})

		// binary payload
		bin := "a\x00b\r\nc\xff"
		pub.Publish(ctx, ch2, bin)
		msg, _ = sub.ReceiveMessage(ctx)
		eq(t, "binary payload", msg.Payload, bin)

		// PUBSUB introspection
		chans, err := pub.PubSubChannels(ctx, k("ch")+"*").Result()
		ck(t, err, "PUBSUB CHANNELS")
		eqUnordered(t, "PUBSUB CHANNELS", chans, []string{ch1, ch2})
		num, err := pub.PubSubNumSub(ctx, ch1, k("nobody")).Result()
		ck(t, err, "PUBSUB NUMSUB")
		eq(t, "PUBSUB NUMSUB", num, map[string]string{ch1: "1", k("nobody"): "0"})
		np, _ := pub.PubSubNumPat(ctx).Result()
		if np < 1 {
			t.Errorf("PUBSUB NUMPAT = %d", np)
		}

		// unsubscribe stops delivery
		ck(t, sub.Unsubscribe(ctx, ch1), "UNSUBSCRIBE")
		m, err = sub.ReceiveTimeout(ctx)
		ck(t, err, "unsubscribe confirmation")
		if s, ok := m.(*hotkv.Subscription); !ok || s.Kind != "unsubscribe" || s.Channel != ch1 {
			t.Fatalf("confirmation = %#v", m)
		}
		n, _ := pub.Publish(ctx, ch1, "nobody listens").Result()
		eq(t, "PUBLISH to an unsubscribed channel", n, int64(0))

		// pattern unsubscribe
		ck(t, sub.PUnsubscribe(ctx, k("pat")+"*"), "PUNSUBSCRIBE")
		m, err = sub.ReceiveTimeout(ctx)
		ck(t, err, "punsubscribe confirmation")
		if s, ok := m.(*hotkv.Subscription); !ok || s.Kind != "punsubscribe" || s.Channel != k("pat")+"*" {
			t.Fatalf("confirmation = %#v", m)
		}
		n2, _ := pub.Publish(ctx, k("pat")+"x", "after punsubscribe").Result()
		eq(t, "PUBLISH to a pattern nobody follows", n2, int64(0))

		// PING on a subscribed connection
		ck(t, sub.Ping(ctx, "payload"), "PubSub.Ping")
		m, err = sub.ReceiveTimeout(ctx)
		ck(t, err, "pong")
		if p, ok := m.(*hotkv.Pong); !ok || (p.Payload != "payload" && p.Payload != "") {
			t.Errorf("pong = %#v", m)
		}
	})
}

func TestLivePubSubChannelAndClose(t *testing.T) {
	forProtocols(t, func(t *testing.T, pub *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		opts := liveOptions(t)
		opts.Protocol = pub.Options().Protocol
		subClient, _ := hotkv.NewClient(opts)
		defer subClient.Close()
		ch := k("chan")
		sub := subClient.Subscribe(ctx, ch)
		msgs := sub.Channel()
		time.Sleep(100 * time.Millisecond)
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				pub.Publish(ctx, ch, fmt.Sprint("m", i))
			}(i)
		}
		wg.Wait()
		got := map[string]bool{}
		timeout := time.After(5 * time.Second)
		for len(got) < 50 {
			select {
			case m := <-msgs:
				got[m.Payload] = true
			case <-timeout:
				t.Fatalf("received %d of 50 messages through Channel()", len(got))
			}
		}
		// Close must end the channel (documented) without panicking.
		done := make(chan struct{})
		go func() {
			sub.Close()
			for range msgs {
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Channel() was not closed by PubSub.Close")
		}
	})
}

func TestLivePipelineAndTransactions(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		p := c.Pipeline()
		set := p.Set(ctx, k("p1"), "v1")
		inc := p.Incr(ctx, k("pc"))
		inc2 := p.Incr(ctx, k("pc"))
		get := p.Get(ctx, k("p1"))
		missing := p.Get(ctx, k("p-none"))
		hs := p.HSet(ctx, k("ph"), "f", "v", "g", "w")
		hg := p.HGet(ctx, k("ph"), "g")
		lp := p.LPush(ctx, k("pl"), "a", "b")
		rp := p.RPush(ctx, k("pl"), "c")
		sa := p.SAdd(ctx, k("ps"), "x", "y")
		ex := p.Expire(ctx, k("p1"), "100")
		bad := p.Do(ctx, "INCR", k("p1")) // WRONGTYPE-free: "v1" is not an integer -> error
		del := p.Del(ctx, k("p-none"))
		dop := p.Do(ctx, "ECHO", "via-do")
		res, err := p.Exec(ctx)
		ck(t, err, "pipeline Exec")
		eq(t, "result count", len(res), 14)
		if s, _ := set.Result(); s != "OK" {
			t.Errorf("SET = %q", s)
		}
		if n, _ := inc.Result(); n != 1 {
			t.Errorf("INCR 1 = %d", n)
		}
		if n, _ := inc2.Result(); n != 2 {
			t.Errorf("INCR 2 = %d", n)
		}
		if s, _ := get.Result(); s != "v1" {
			t.Errorf("GET = %q", s)
		}
		_, err = missing.Result()
		isNil(t, "pipelined GET of a missing key", err)
		if n, _ := hs.Result(); n != 2 {
			t.Errorf("HSET = %d", n)
		}
		if s, _ := hg.Result(); s != "w" {
			t.Errorf("HGET = %q", s)
		}
		if n, _ := lp.Result(); n != 2 {
			t.Errorf("LPUSH = %d", n)
		}
		if n, _ := rp.Result(); n != 3 {
			t.Errorf("RPUSH = %d", n)
		}
		if n, _ := sa.Result(); n != 2 {
			t.Errorf("SADD = %d", n)
		}
		if b, _ := ex.Result(); !b {
			t.Errorf("EXPIRE = %v", b)
		}
		if bad.Err() == nil {
			t.Error("pipelined INCR of a non-integer must carry the server error on its own Cmd")
		}
		if n, _ := del.Result(); n != 0 {
			t.Errorf("DEL = %d", n)
		}
		if s, _ := dop.Text(); s != "via-do" {
			t.Errorf("Do ECHO = %q", s)
		}
		eq(t, "pipeline is empty after Exec", p.Len(), 0)

		// MULTI/EXEC
		tx := c.TxPipeline()
		a := tx.Incr(ctx, k("tx"))
		b := tx.Incr(ctx, k("tx"))
		g := tx.Get(ctx, k("tx"))
		_, err = tx.Exec(ctx)
		ck(t, err, "tx Exec")
		na, _ := a.Result()
		nb, _ := b.Result()
		gs, _ := g.Result()
		eq(t, "tx results", []interface{}{na, nb, gs}, []interface{}{int64(1), int64(2), "2"})

		// WATCH: a concurrent write aborts the transaction
		wk := k("watched")
		ck(t, c.Set(ctx, wk, "0", 0).Err(), "SET watched")
		err = c.Watch(ctx, func(txn *hotkv.Tx) error {
			cur, err := txn.Get(ctx, wk).Result()
			ck(t, err, "GET in WATCH")
			eq(t, "value seen inside Watch", cur, "0")
			ck(t, c.Set(ctx, wk, "changed-by-someone-else", 0).Err(), "concurrent SET")
			pp := txn.Pipeline()
			pp.Set(ctx, wk, "from-tx")
			_, err = pp.Exec(ctx)
			return err
		}, wk)
		if err != hotkv.TxFailedErr {
			t.Errorf("Watch with a concurrent write: err = %v, want TxFailedErr", err)
		}
		v, _ := c.Get(ctx, wk).Result()
		eq(t, "value after the aborted transaction", v, "changed-by-someone-else")
		// and it commits when nothing interferes
		err = c.Watch(ctx, func(txn *hotkv.Tx) error {
			pp := txn.Pipeline()
			pp.Set(ctx, wk, "from-tx")
			_, err := pp.Exec(ctx)
			return err
		}, wk)
		ck(t, err, "uncontended Watch")
		v, _ = c.Get(ctx, wk).Result()
		eq(t, "value after the committed transaction", v, "from-tx")
		// the pooled connection used by Watch must not keep a watch
		ck(t, c.Set(ctx, wk, "again", 0).Err(), "SET again")
		tx2 := c.TxPipeline()
		tx2.Incr(ctx, k("after-watch"))
		if _, err := tx2.Exec(ctx); err != nil {
			t.Errorf("a leftover WATCH aborted an unrelated transaction: %v", err)
		}
	})
}

func TestLiveConcurrentUse(t *testing.T) {
	c, k := liveClient(t, 3)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := k(fmt.Sprintf("conc:%d:%d", g, i))
				if err := c.Set(ctx, key, key, 10*time.Minute).Err(); err != nil {
					errs <- err
					return
				}
				v, err := c.Get(ctx, key).Result()
				if err != nil || v != key {
					errs <- fmt.Errorf("GET %s = %q, %v", key, v, err)
					return
				}
				if _, err := c.Incr(ctx, k("conc-counter")).Result(); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	v, _ := c.Get(ctx, k("conc-counter")).Result()
	eq(t, "concurrent INCR total", v, "3200")
	if ps := c.PoolStats(); ps.TotalConns > 10 {
		t.Errorf("pool grew past PoolSize: %+v", ps)
	}
}

func TestLiveDoGenericCommand(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		s, err := c.Do(ctx, "SET", k("do"), "v").Text()
		ck(t, err, "Do SET")
		eq(t, "Do SET", s, "OK")
		n, err := c.Do(ctx, "STRLEN", k("do")).Int64()
		ck(t, err, "Do STRLEN")
		eq(t, "Do STRLEN", n, int64(1))
		_, err = c.Do(ctx, "GET", k("do-none")).Result()
		isNil(t, "Do GET missing", err)
		if err := c.Do(ctx, "NOSUCHCOMMAND").Err(); err == nil {
			t.Error("unknown command must be an error")
		}
		var herr *hotkv.Error
		err = c.Do(ctx, "INCR", k("do")).Err()
		if e, ok := err.(*hotkv.Error); ok {
			herr = e
		}
		if herr == nil || herr.Prefix() != "ERR" {
			t.Errorf("INCR of a non-integer: %v", err)
		}
		// typed accessors over several reply shapes
		ck(t, c.Do(ctx, "RPUSH", k("dol"), "a", "b").Err(), "Do RPUSH")
		ss, err := c.Do(ctx, "LRANGE", k("dol"), 0, -1).StringSlice()
		ck(t, err, "Do LRANGE")
		eq(t, "Do LRANGE", ss, []string{"a", "b"})
		sl, _ := c.Do(ctx, "LRANGE", k("dol"), 0, -1).Slice()
		eq(t, "Do LRANGE Slice", sl, []interface{}{"a", "b"})
		bs, _ := c.Do(ctx, "SET", k("bin"), []byte{0, 255, 10}).Result()
		eq(t, "binary arg accepted", bs, "OK")
		got, _ := c.Do(ctx, "GET", k("bin")).Text()
		eq(t, "binary arg round trip", got, "\x00\xff\n")
		f, err := c.Do(ctx, "INCRBYFLOAT", k("dof"), 2.5).Float64()
		ck(t, err, "Do INCRBYFLOAT")
		eq(t, "Do INCRBYFLOAT", f, 2.5)
		b, _ := c.Do(ctx, "EXISTS", k("do")).Bool()
		eq(t, "Do EXISTS", b, true)
		// argument conversion errors send nothing
		if err := c.Do(ctx, "SET", k("x"), struct{}{}).Err(); err == nil {
			t.Error("unsupported argument type accepted")
		}
		if err := c.Do(ctx).Err(); err == nil {
			t.Error("Do with no command accepted")
		}
	})
}
