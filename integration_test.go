// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

// TestIntegration exercises every core command family plus every HotKV
// exclusive and enterprise/AI command family against a live server. It runs
// only when HOTKV_TEST_URL is set (e.g. HOTKV_TEST_URL=hotkv://127.0.0.1:6379);
// otherwise the whole test, and every subtest, is skipped. Enterprise
// commands that reply NOLICENSE because the server license does not cover
// them are skipped individually, so this suite starts passing those subtests
// automatically once a license that covers them is used.
func TestIntegration(t *testing.T) {
	rawURL := os.Getenv("HOTKV_TEST_URL")
	if rawURL == "" {
		t.Skip("HOTKV_TEST_URL not set; skipping integration tests (e.g. hotkv://127.0.0.1:6379)")
	}

	opts, err := hotkv.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("invalid HOTKV_TEST_URL %q: %v", rawURL, err)
	}
	client, err := hotkv.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("server not reachable at %s: %v", rawURL, err)
	}

	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	key := func(suffix string) string { return "sdktest:" + runID + ":" + suffix }

	requireOK := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			return
		}
		if hotkv.IsNoLicenseError(err) {
			t.Skipf("license does not cover this feature: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("Core", func(t *testing.T) {
		requireOK(t, client.Set(ctx, key("str"), "v1", 0).Err())
		v, err := client.Get(ctx, key("str")).Result()
		requireOK(t, err)
		if v != "v1" {
			t.Errorf("GET = %q, want v1", v)
		}
		requireOK(t, client.Expire(ctx, key("str"), time.Hour).Err())
		requireOK(t, client.SAdd(ctx, key("set"), "a", "b").Err())
		requireOK(t, client.HSet(ctx, key("hash"), "f1", "v1").Err())
		requireOK(t, client.ZAdd(ctx, key("zset"), hotkv.Z{Score: 1, Member: "m1"}).Err())
		requireOK(t, client.LPush(ctx, key("list"), "x", "y").Err())
		requireOK(t, client.Ping(ctx).Err())
	})

	t.Run("RESP2AndRESP3", func(t *testing.T) {
		opts2, err := hotkv.ParseURL(rawURL)
		if err != nil {
			t.Fatalf("invalid HOTKV_TEST_URL %q: %v", rawURL, err)
		}
		opts2.Protocol = 2
		client2, err := hotkv.NewClient(opts2)
		if err != nil {
			t.Fatalf("NewClient (RESP2): %v", err)
		}
		t.Cleanup(func() { client2.Close() })
		if err := client2.Ping(ctx).Err(); err != nil {
			t.Fatalf("RESP2 client not reachable: %v", err)
		}

		protocols := []struct {
			name string
			cl   *hotkv.Client
		}{
			{"RESP3", client},
			{"RESP2", client2},
		}
		for _, proto := range protocols {
			proto := proto
			t.Run(proto.name, func(t *testing.T) {
				cl := proto.cl
				p := key("conform" + proto.name)

				// HGETALL
				hkey := p + ":hash"
				requireOK(t, cl.HSet(ctx, hkey, "f1", "v1", "f2", "v2").Err())
				hm, err := cl.HGetAll(ctx, hkey).Result()
				requireOK(t, err)
				if len(hm) != 2 || hm["f1"] != "v1" || hm["f2"] != "v2" {
					t.Errorf("HGETALL = %v, want map[f1:v1 f2:v2]", hm)
				}

				// HRANDFIELD WITHVALUES
				kvs, err := cl.HRandFieldWithValues(ctx, hkey, 2).Result()
				requireOK(t, err)
				if len(kvs) != 2 {
					t.Errorf("HRANDFIELD WITHVALUES len = %d, want 2", len(kvs))
				}
				seenFV := map[string]string{}
				for _, kv := range kvs {
					seenFV[kv.Key] = kv.Value
				}
				if seenFV["f1"] != "v1" || seenFV["f2"] != "v2" {
					t.Errorf("HRANDFIELD WITHVALUES = %+v, want f1:v1 f2:v2", kvs)
				}

				// SMEMBERS
				skey := p + ":set"
				requireOK(t, cl.SAdd(ctx, skey, "a", "b", "c").Err())
				members, err := cl.SMembers(ctx, skey).Result()
				requireOK(t, err)
				seenS := map[string]bool{}
				for _, m := range members {
					seenS[m] = true
				}
				if len(members) != 3 || !seenS["a"] || !seenS["b"] || !seenS["c"] {
					t.Errorf("SMEMBERS = %v, want {a b c}", members)
				}

				// ZSCORE / ZRANGE WITHSCORES / ZPOPMIN with count / ZRANDMEMBER WITHSCORES
				zkey := p + ":zset"
				requireOK(t, cl.ZAdd(ctx, zkey,
					hotkv.Z{Score: 1, Member: "a"},
					hotkv.Z{Score: 2, Member: "b"},
					hotkv.Z{Score: 3, Member: "c"},
				).Err())
				score, err := cl.ZScore(ctx, zkey, "b").Result()
				requireOK(t, err)
				if score != 2 {
					t.Errorf("ZSCORE b = %v, want 2", score)
				}
				zs, err := cl.ZRangeWithScores(ctx, zkey, 0, -1).Result()
				requireOK(t, err)
				wantZS := []hotkv.Z{{Score: 1, Member: "a"}, {Score: 2, Member: "b"}, {Score: 3, Member: "c"}}
				if len(zs) != len(wantZS) {
					t.Errorf("ZRANGE WITHSCORES = %+v, want %+v", zs, wantZS)
				} else {
					for i := range zs {
						if zs[i] != wantZS[i] {
							t.Errorf("ZRANGE WITHSCORES[%d] = %+v, want %+v", i, zs[i], wantZS[i])
						}
					}
				}
				popped, err := cl.ZPopMin(ctx, zkey, 2).Result()
				requireOK(t, err)
				if len(popped) != 2 || popped[0].Member != "a" || popped[0].Score != 1 || popped[1].Member != "b" || popped[1].Score != 2 {
					t.Errorf("ZPOPMIN count=2 = %+v, want [{1 a} {2 b}]", popped)
				}
				requireOK(t, cl.ZAdd(ctx, zkey, hotkv.Z{Score: 1, Member: "a"}, hotkv.Z{Score: 2, Member: "b"}).Err())
				randScored, err := cl.ZRandMemberWithScores(ctx, zkey, 3).Result()
				requireOK(t, err)
				if len(randScored) != 3 {
					t.Errorf("ZRANDMEMBER WITHSCORES len = %d, want 3", len(randScored))
				}

				// XREAD / XINFO STREAM
				xkey := p + ":stream"
				id1, err := cl.XAdd(ctx, xkey, "*", "field1", "v1").Result()
				requireOK(t, err)
				id2, err := cl.XAdd(ctx, xkey, "*", "field2", "v2").Result()
				requireOK(t, err)
				streams, err := cl.XRead(ctx, 0, map[string]string{xkey: "0"}).Result()
				requireOK(t, err)
				if len(streams) != 1 || streams[0].Stream != xkey || len(streams[0].Entries) != 2 {
					t.Fatalf("XREAD = %+v, want 1 stream %q with 2 entries", streams, xkey)
				}
				if streams[0].Entries[0].ID != id1 || streams[0].Entries[0].Fields["field1"] != "v1" {
					t.Errorf("XREAD entry[0] = %+v, want id=%s field1=v1", streams[0].Entries[0], id1)
				}
				if streams[0].Entries[1].ID != id2 || streams[0].Entries[1].Fields["field2"] != "v2" {
					t.Errorf("XREAD entry[1] = %+v, want id=%s field2=v2", streams[0].Entries[1], id2)
				}
				info, err := cl.XInfoStream(ctx, xkey).Result()
				requireOK(t, err)
				if l, ok := info["length"]; !ok || l.Int != 2 {
					t.Errorf("XINFO STREAM length = %+v, want 2", info["length"])
				}
				if lg, ok := info["last-generated-id"]; !ok || lg.Str != id2 {
					t.Errorf("XINFO STREAM last-generated-id = %+v, want %s", info["last-generated-id"], id2)
				}

				// CONFIG GET
				cfg, err := cl.ConfigGet(ctx, "maxmemory").Result()
				requireOK(t, err)
				if _, ok := cfg["maxmemory"]; !ok {
					t.Errorf("CONFIG GET maxmemory missing from %v", cfg)
				}

				// INFO (RESP3 replies with a verbatim string; must decode to
				// the same plain string as the RESP2 bulk string, with no
				// leftover "txt:" encoding prefix).
				infoStr, err := cl.Info(ctx).Result()
				requireOK(t, err)
				if strings.HasPrefix(infoStr, "txt:") || strings.HasPrefix(infoStr, "mkd:") {
					t.Errorf("INFO leaked a verbatim string encoding prefix: %.20q", infoStr)
				}
				if !strings.Contains(infoStr, "hotkv_version") {
					t.Errorf("INFO = %.60q, want it to contain hotkv_version", infoStr)
				}
			})
		}
	})

	t.Run("PipelineAndTx", func(t *testing.T) {
		pipe := client.Pipeline()
		setCmd := pipe.Set(ctx, key("pl1"), "a")
		getCmd := pipe.Get(ctx, key("pl1"))
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatalf("pipeline exec: %v", err)
		}
		requireOK(t, setCmd.Err())
		if v, err := getCmd.Result(); err != nil || v != "a" {
			t.Errorf("pipelined GET = %q, %v", v, err)
		}

		tx := client.TxPipeline()
		tx.Set(ctx, key("tx1"), "v")
		if _, err := tx.Exec(ctx); err != nil {
			t.Fatalf("tx exec: %v", err)
		}
	})

	t.Run("PubSub", func(t *testing.T) {
		ps := client.Subscribe(ctx, key("chan"))
		defer ps.Close()
		confirmCtx, confirmCancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := ps.ReceiveTimeout(confirmCtx)
		confirmCancel()
		if err != nil {
			t.Fatalf("subscribe confirmation: %v", err)
		}
		if err := client.Publish(ctx, key("chan"), "hello").Err(); err != nil {
			t.Fatalf("PUBLISH: %v", err)
		}
		msgCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		msg, err := ps.ReceiveMessage(msgCtx)
		if err != nil {
			t.Fatalf("ReceiveMessage: %v", err)
		}
		if msg.Payload != "hello" {
			t.Errorf("Payload = %q, want hello", msg.Payload)
		}
	})

	t.Run("TagNExpireHotkeys", func(t *testing.T) {
		requireOK(t, client.Set(ctx, key("tagged"), "v", 0).Err())
		_, err := client.TagSet(ctx, key("tagged"), "alpha", "beta").Result()
		requireOK(t, err)
		if _, err := client.TagGet(ctx, key("tagged")).Result(); err != nil {
			t.Errorf("TagGet: %v", err)
		}
		if _, err := client.TagMembers(ctx, "alpha").Result(); err != nil {
			t.Errorf("TagMembers: %v", err)
		}
		requireOK(t, client.NExpire(ctx, key("tagged"), 5*time.Second.Nanoseconds()).Err())
		if _, err := client.NTTL(ctx, key("tagged")).Result(); err != nil {
			t.Errorf("NTTL: %v", err)
		}
		if _, err := client.HotKeysTop(ctx, 5).Result(); err != nil {
			t.Errorf("HotKeysTop: %v", err)
		}
	})

	t.Run("GCRA", func(t *testing.T) {
		res, err := client.GCRALimit(ctx, key("gcra"), 10*time.Second, 5, 0).Result()
		requireOK(t, err)
		if res.Limit != 5 {
			t.Errorf("GCRAResult.Limit = %d, want 5", res.Limit)
		}
		requireOK(t, client.GCRAReset(ctx, key("gcra")).Err())
	})

	t.Run("VSIM", func(t *testing.T) {
		idx := key("vsimidx")
		vec := []float32{0.1, 0.2, 0.3}
		requireOK(t, client.VSIMCreateIndex(ctx, idx, 3, nil).Err())
		requireOK(t, client.VSIMSet(ctx, idx, "v1", vec).Err())
		if _, err := client.VSIMGet(ctx, idx, "v1").Result(); err != nil {
			t.Errorf("VSIMGet: %v", err)
		}
		if _, err := client.VSIMSearch(ctx, idx, vec, 1, nil).Result(); err != nil {
			t.Errorf("VSIMSearch: %v", err)
		}
		info, err := client.VSIMInfo(ctx, idx).Result()
		requireOK(t, err)
		if info.Dimensions != 3 {
			t.Errorf("VSIMInfo.Dimensions = %d, want 3", info.Dimensions)
		}
		if _, err := client.VSIMIndexNames(ctx).Result(); err != nil {
			t.Errorf("VSIMIndexNames: %v", err)
		}
		requireOK(t, client.VSIMDel(ctx, idx, "v1").Err())
		requireOK(t, client.VSIMDropIndex(ctx, idx).Err())

		t.Run("Persistence", func(t *testing.T) {
			// snapshot names may not contain ':' or '.'
			snap := "sdktest_" + strings.NewReplacer(":", "_", ".", "_").Replace(runID) + "_snap"
			_, err := client.VSIMSave(ctx, snap, []byte("blob")).Result()
			requireOK(t, err)
			requireOK(t, client.VSIMSnapshotDrop(ctx, snap).Err())
		})
	})

	t.Run("SCache", func(t *testing.T) {
		ns := key("scache")
		vec := []float32{0.1, 0.2, 0.3}
		th := 0.5
		requireOK(t, client.SCacheCreate(ctx, ns, 3, &hotkv.SCacheCreateOptions{Threshold: &th}).Err())
		usage := &hotkv.Usage{Model: "gpt-4", Input: 12, Output: 8}
		requireOK(t, client.SCacheSet(ctx, ns, "q1", "r1", vec, 0, usage).Err())
		res, err := client.SCacheGet(ctx, ns, vec, nil).Result()
		requireOK(t, err)
		if !res.Hit || res.Response != "r1" {
			t.Errorf("SCacheGet = %+v, want hit r1", res)
		}
		stats, err := client.SCacheStats(ctx, ns).Result()
		requireOK(t, err)
		if stats.Entries != 1 {
			t.Errorf("SCacheStats.Entries = %d, want 1", stats.Entries)
		}
		requireOK(t, client.SCacheDrop(ctx, ns).Err())
	})

	t.Run("PCache", func(t *testing.T) {
		ns := key("pcache")
		requireOK(t, client.PCacheCreate(ctx, ns, 0, 0, 0).Err())
		usage := &hotkv.Usage{Model: "gpt-4", Input: 12, Output: 8}
		requireOK(t, client.PCacheSet(ctx, ns, "p1", "r1", 0, "", usage).Err())
		v, err := client.PCacheGet(ctx, ns, "p1").Result()
		requireOK(t, err)
		if v != "r1" {
			t.Errorf("PCacheGet = %q, want r1", v)
		}
		stats, err := client.PCacheStats(ctx, ns).Result()
		requireOK(t, err)
		if stats.Entries != 1 {
			t.Errorf("PCacheStats.Entries = %d, want 1", stats.Entries)
		}
		requireOK(t, client.PCacheDrop(ctx, ns).Err())
	})

	t.Run("KeyedCaches", func(t *testing.T) {
		families := []struct {
			name   string
			create func(context.Context, string, uint64, time.Duration) *hotkv.StatusCmd
			set    func(context.Context, string, string, string, time.Duration, string, *hotkv.Usage) *hotkv.StatusCmd
			get    func(context.Context, string, string) *hotkv.StringCmd
			stats  func(context.Context, string) *hotkv.KeyedCacheStatsCmd
			drop   func(context.Context, string) *hotkv.BoolCmd
		}{
			{"TOOL.CACHE", client.ToolCacheCreate, client.ToolCacheSet, client.ToolCacheGet, client.ToolCacheStats, client.ToolCacheDrop},
			{"NCACHE", client.NCacheCreate, client.NCacheSet, client.NCacheGet, client.NCacheStats, client.NCacheDrop},
			{"GUARD", client.GuardCacheCreate, client.GuardCacheSet, client.GuardCacheGet, client.GuardCacheStats, client.GuardCacheDrop},
			{"RERANK", client.RerankCacheCreate, client.RerankCacheSet, client.RerankCacheGet, client.RerankCacheStats, client.RerankCacheDrop},
		}
		for _, f := range families {
			f := f
			t.Run(f.name, func(t *testing.T) {
				ns := key(f.name)
				requireOK(t, f.create(ctx, ns, 0, 0).Err())
				requireOK(t, f.set(ctx, ns, "k1", "v1", 0, "", &hotkv.Usage{Model: "gpt-4", Input: 5}).Err())
				v, err := f.get(ctx, ns, "k1").Result()
				requireOK(t, err)
				if v != "v1" {
					t.Errorf("%s.GET = %q, want v1", f.name, v)
				}
				stats, err := f.stats(ctx, ns).Result()
				requireOK(t, err)
				if stats.Entries != 1 {
					t.Errorf("%s.STATS.Entries = %d, want 1", f.name, stats.Entries)
				}
				requireOK(t, f.drop(ctx, ns).Err())
			})
		}
	})

	t.Run("LLMStats", func(t *testing.T) {
		bucket := key("llmstats")
		requireOK(t, client.LLMStatsBucket(ctx, bucket).Err())
		requireOK(t, client.LLMStatsRecord(ctx, bucket, 100, 50, 0, 0.002, 120*time.Millisecond, nil).Err())
		requireOK(t, client.LLMStatsRecord(ctx, bucket, 100, 50, 10, 0, 80*time.Millisecond,
			&hotkv.LLMStatsRecordOptions{Model: "gpt-4", CacheWrite5m: 20}).Err())
		stats, err := client.LLMStatsGet(ctx, bucket).Result()
		requireOK(t, err)
		if stats.Requests != 2 {
			t.Errorf("LLMStatsGet.Requests = %d, want 2", stats.Requests)
		}
		requireOK(t, client.LLMStatsDrop(ctx, bucket).Err())

		t.Run("Price", func(t *testing.T) {
			model := key("model")
			requireOK(t, client.LLMStatsPriceSet(ctx, model, 3, 15, nil).Err())
			price, err := client.LLMStatsPriceGet(ctx, model).Result()
			requireOK(t, err)
			if price.Source != "custom" || !price.Priced {
				t.Errorf("LLMStatsPriceGet(%s) = %+v, want a priced custom entry", model, price)
			}
			if _, err := client.LLMStatsPriceList(ctx).Result(); err != nil {
				requireOK(t, err)
			}
			deleted, err := client.LLMStatsPriceDel(ctx, model).Result()
			requireOK(t, err)
			if !deleted {
				t.Error("LLMStatsPriceDel should report the custom override was removed")
			}
		})
	})

	t.Run("LLMBudget", func(t *testing.T) {
		name := key("llmbudget")
		requireOK(t, client.LLMBudgetSet(ctx, name, 1000, 5.0, time.Hour).Err())
		allowed, err := client.LLMBudgetConsume(ctx, name, 10, 0.01, nil).Result()
		requireOK(t, err)
		if !allowed {
			t.Error("LLMBudgetConsume should be allowed under a fresh budget")
		}
		allowed2, err := client.LLMBudgetConsume(ctx, name, 0, 0, &hotkv.Usage{Model: "gpt-4", Input: 10, Output: 5}).Result()
		requireOK(t, err)
		if !allowed2 {
			t.Error("LLMBudgetConsume with usage should be allowed under a fresh budget")
		}
		status, err := client.LLMBudgetStatus(ctx, name).Result()
		requireOK(t, err)
		if status.TokenLimit != 1000 {
			t.Errorf("LLMBudgetStatus.TokenLimit = %d, want 1000", status.TokenLimit)
		}
		requireOK(t, client.LLMBudgetDrop(ctx, name).Err())
	})

	t.Run("Prompt", func(t *testing.T) {
		name := key("prompt")
		v, err := client.PromptPut(ctx, name, "hello {{name}}", "").Result()
		requireOK(t, err)
		if v != 1 {
			t.Errorf("PromptPut version = %d, want 1", v)
		}
		if _, err := client.PromptLatest(ctx, name).Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.PromptDrop(ctx, name).Err())
	})

	t.Run("EmbedDedup", func(t *testing.T) {
		ns := key("embed")
		stored, err := client.EmbedDedupPut(ctx, ns, "hash1", "modelA", "0.1,0.2").Result()
		requireOK(t, err)
		if !stored {
			t.Error("EmbedDedupPut should report newly stored on first put")
		}
		requireOK(t, client.EmbedDedupDrop(ctx, ns).Err())
	})

	t.Run("Amem", func(t *testing.T) {
		sess := key("sess")
		agent := key("agent")
		vec := []float32{0.1, 0.2, 0.3}

		requireOK(t, client.AmemConvCreate(ctx, sess, 0, 0).Err())
		requireOK(t, client.AmemConvAppend(ctx, sess, "user", "hi").Err())
		if _, err := client.AmemConvGet(ctx, sess).Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.AmemConvDrop(ctx, sess).Err())

		requireOK(t, client.AmemSemCreate(ctx, agent, 3).Err())
		requireOK(t, client.AmemSemStore(ctx, agent, "k1", "fact", vec, 0).Err())
		if _, err := client.AmemSemRecall(ctx, agent, 1, vec).Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.AmemSemForget(ctx, agent, "k1").Err())

		requireOK(t, client.AmemWorkSet(ctx, sess, "wk", "wv", 0).Err())
		if v, err := client.AmemWorkGet(ctx, sess, "wk").Result(); err != nil || v != "wv" {
			t.Errorf("AmemWorkGet = %q, %v", v, err)
		}
		requireOK(t, client.AmemWorkClear(ctx, sess).Err())

		requireOK(t, client.AmemEpisLog(ctx, agent, "note", "content", "").Err())
		if _, err := client.AmemEpisQuery(ctx, agent, nil).Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.AmemEpisClear(ctx, agent).Err())

		if _, err := client.AmemSearch(ctx, agent, 1, vec).Result(); err != nil {
			requireOK(t, err)
		}

		t.Run("Summary", func(t *testing.T) {
			v, err := client.AmemSummarySet(ctx, agent, "summary v1").Result()
			requireOK(t, err)
			if v != 1 {
				t.Errorf("AmemSummarySet version = %d, want 1", v)
			}
			requireOK(t, client.AmemSummaryDrop(ctx, agent).Err())
		})
	})

	t.Run("RAG", func(t *testing.T) {
		pipeline := key("rag")
		requireOK(t, client.RAGCreate(ctx, pipeline, 3, nil).Err())
		n, err := client.RAGIngest(ctx, pipeline, "doc1", "hello world", nil,
			[][]float32{{0.1, 0.2, 0.3}}).Result()
		requireOK(t, err)
		if n != 1 {
			t.Errorf("RAGIngest chunks = %d, want 1", n)
		}
		if _, err := client.RAGSearch(ctx, pipeline, "hello", nil).Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.RAGDrop(ctx, pipeline).Err())
	})

	t.Run("RateLimit", func(t *testing.T) {
		name := key("rl")
		requireOK(t, client.RateLimitCreate(ctx, name, 10, time.Second, nil).Err())
		res, err := client.RateLimitCheck(ctx, name, "clientA", 0).Result()
		requireOK(t, err)
		if !res.Allowed {
			t.Error("first RateLimitCheck should be allowed")
		}
		if _, err := client.RateLimitPeek(ctx, name, "clientA").Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.RateLimitDrop(ctx, name).Err())
	})

	t.Run("TS", func(t *testing.T) {
		series := key("ts")
		requireOK(t, client.TSCreate(ctx, series, 0, nil).Err())
		if _, err := client.TSAdd(ctx, series, nil, 1.5).Result(); err != nil {
			requireOK(t, err)
		}
		if _, err := client.TSRange(ctx, series, 0, time.Now().UnixMilli()+1000, 0).Result(); err != nil {
			requireOK(t, err)
		}
		if _, err := client.TSInfo(ctx, series).Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.TSDelete(ctx, series).Err())
	})

	t.Run("Feature", func(t *testing.T) {
		group := key("feat")
		requireOK(t, client.FeatureCreate(ctx, group, "desc", map[string]string{"score": "int64"}).Err())
		requireOK(t, client.FeatureSet(ctx, group, "e1", map[string]string{"score": "42"}).Err())
		if _, err := client.FeatureGet(ctx, group, "e1").Result(); err != nil {
			requireOK(t, err)
		}
		requireOK(t, client.FeatureDrop(ctx, group).Err())
	})

	t.Run("PromptCacheHelper", func(t *testing.T) {
		pc := hotkv.NewPromptCache(client, key("pc-helper"), time.Minute).WithStatsBucket(key("pc-helper-stats"))
		calls := 0
		call := func(context.Context) (hotkv.LLMCallResult, error) {
			calls++
			return hotkv.LLMCallResult{Response: "42", Usage: hotkv.LLMUsage{Model: "test-model", InputTokens: 5, OutputTokens: 1}}, nil
		}
		in := hotkv.PromptCacheKeyInput{Provider: "test", Model: "test-model",
			Messages: []hotkv.PromptMessage{{Role: "user", Content: "what is the answer?"}}}

		resp, hit, err := pc.GetOrCall(ctx, in, call)
		requireOK(t, err)
		if hit {
			t.Error("first call should be a miss")
		}
		if resp != "42" {
			t.Errorf("response = %q, want 42", resp)
		}
		if calls != 1 {
			t.Errorf("fn called %d times, want 1", calls)
		}

		resp2, hit2, err2 := pc.GetOrCall(ctx, in, call)
		if err2 != nil && hotkv.IsNoLicenseError(err2) {
			t.Skipf("PromptCache namespace unavailable: %v", err2)
		}
		requireOK(t, err2)
		if !hit2 {
			// Without PromptCache license the namespace never persisted,
			// so a second miss (and a second fn call) is expected.
			if calls != 2 {
				t.Errorf("expected a second fn call on a cache-less miss, got %d total calls", calls)
			}
		} else if resp2 != "42" {
			t.Errorf("cached response = %q, want 42", resp2)
		}

		if _, serr := pc.Stats(ctx); serr != nil && !hotkv.IsNoLicenseError(serr) {
			t.Errorf("PromptCache.Stats: %v", serr)
		}
	})

	t.Run("SemanticCacheHelper", func(t *testing.T) {
		sc := hotkv.NewSemanticCache(client, key("sc-helper"), 0)
		requireOK(t, sc.CreateNamespace(ctx, 3, "cosine", nil))
		vec := []float32{0.4, 0.5, 0.6}
		calls := 0
		call := func(context.Context) (hotkv.LLMCallResult, error) {
			calls++
			return hotkv.LLMCallResult{Response: "paris", Usage: hotkv.LLMUsage{Model: "gpt-4", InputTokens: 6, OutputTokens: 1}}, nil
		}
		resp, hit, err := sc.GetOrCall(ctx, "capital of france?", vec, call)
		requireOK(t, err)
		if hit || resp != "paris" || calls != 1 {
			t.Errorf("first GetOrCall: hit=%v resp=%q calls=%d, want miss/paris/1", hit, resp, calls)
		}
		resp2, hit2, err2 := sc.GetOrCall(ctx, "capital of france?", vec, call)
		requireOK(t, err2)
		if !hit2 || resp2 != "paris" || calls != 1 {
			t.Errorf("second GetOrCall: hit=%v resp=%q calls=%d, want hit/paris/1", hit2, resp2, calls)
		}
		if _, err := sc.Stats(ctx); err != nil {
			requireOK(t, err)
		}
	})

	t.Run("ObserveAlerts", func(t *testing.T) {
		_, err := client.ObserveInfo(ctx).Result()
		requireOK(t, err)
		_, err = client.AlertsInfo(ctx).Result()
		requireOK(t, err)
	})
}
