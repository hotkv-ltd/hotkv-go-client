// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

func TestLiveLLMStats(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		b := k("llmstats")
		ck(t, c.LLMStatsBucket(ctx, b).Err(), "LLMSTATS.BUCKET")
		defer c.LLMStatsDrop(ctx, b)
		// plain cost, including one written as an exponent by other SDKs
		ck(t, c.LLMStatsRecord(ctx, b, 100, 50, 0, 0.002, 120*time.Millisecond, nil).Err(), "RECORD")
		ck(t, c.LLMStatsRecord(ctx, b, 10, 5, 2, 1e-5, 80*time.Millisecond, nil).Err(), "RECORD tiny cost")
		failed := true
		ck(t, c.LLMStatsRecord(ctx, b, 1, 1, 0, 0, 0, &hotkv.LLMStatsRecordOptions{Error: &failed}).Err(), "RECORD error")
		model := k("m")
		ck(t, c.LLMStatsPriceSet(ctx, model, 3, 15, &hotkv.LLMPriceSetOptions{}).Err(), "PRICE SET")
		defer c.LLMStatsPriceDel(ctx, model)
		ck(t, c.LLMStatsRecord(ctx, b, 1000000, 1000000, 0, 0, time.Millisecond, &hotkv.LLMStatsRecordOptions{Model: model}).Err(), "RECORD MODEL")
		st, err := c.LLMStatsGet(ctx, b).Result()
		ck(t, err, "LLMSTATS.GET")
		eq(t, "requests", st.Requests, int64(4))
		eq(t, "errors", st.Errors, int64(1))
		eq(t, "input tokens", st.InputTokens, int64(100+10+1+1000000))
		eq(t, "output tokens", st.OutputTokens, int64(50+5+1+1000000))
		eq(t, "cached tokens", st.CachedTokens, int64(2))
		// 0.002 + 0.00001 + (3 + 15 USD for 1M + 1M tokens)
		near(t, "cost", st.CostUSD, 18.00201, 1e-6)
		list, _ := c.LLMStatsList(ctx).Result()
		if !has(list, b) {
			t.Errorf("LLMSTATS.LIST = %v", list)
		}
		rs, _ := c.LLMStatsReset(ctx, b).Result()
		eq(t, "RESET", rs, true)
		st, _ = c.LLMStatsGet(ctx, b).Result()
		eq(t, "requests after RESET", st.Requests, int64(0))

		// price table
		pr, err := c.LLMStatsPriceGet(ctx, model).Result()
		ck(t, err, "PRICE GET")
		if pr.Model != model || pr.Source != "custom" || !pr.Priced {
			t.Errorf("PRICE GET = %+v", pr)
		}
		near(t, "input price", pr.InputUSDPerMtok, 3, 1e-9)
		near(t, "output price", pr.OutputUSDPerMtok, 15, 1e-9)
		// prices with 6 decimals, written the way the server accepts them
		ck(t, c.LLMStatsPriceSet(ctx, model, 0.000001, 12.5, &hotkv.LLMPriceSetOptions{
			CacheReadUSDPerMtok:    ptr(1e-5),
			CacheWrite5mUSDPerMtok: ptr(1.25),
			CacheWrite1hUSDPerMtok: ptr(1e7),
		}).Err(), "PRICE SET fractional")
		pr, _ = c.LLMStatsPriceGet(ctx, model).Result()
		near(t, "input price 1e-6", pr.InputUSDPerMtok, 0.000001, 1e-9)
		near(t, "output price 12.5", pr.OutputUSDPerMtok, 12.5, 1e-9)
		near(t, "cache read 1e-5", pr.CacheReadUSDPerMtok, 0.00001, 1e-9)
		near(t, "cache write 5m", pr.CacheWrite5mUSDPerMtok, 1.25, 1e-9)
		near(t, "cache write 1h 1e7", pr.CacheWrite1hUSDPerMtok, 1e7, 1e-3)
		all, err := c.LLMStatsPriceList(ctx).Result()
		ck(t, err, "PRICE LIST")
		found := false
		for _, p := range all {
			if p.Model == model {
				found = true
			}
		}
		if !found || len(all) < 2 {
			t.Errorf("PRICE LIST (%d entries) does not include %s", len(all), model)
		}
		unk, err := c.LLMStatsPriceGet(ctx, k("no-such-model")).Result()
		ck(t, err, "PRICE GET unknown")
		if unk.Priced || unk.Source != "unpriced" {
			t.Errorf("PRICE GET unknown = %+v, want unpriced", unk)
		}
		del, _ := c.LLMStatsPriceDel(ctx, model).Result()
		eq(t, "PRICE DEL", del, true)
		del, _ = c.LLMStatsPriceDel(ctx, model).Result()
		eq(t, "PRICE DEL again", del, false)
		// invalid prices are refused before they reach the wire
		for name, v := range map[string]float64{"negative": -1, "NaN": math.NaN(), "rounds to 0": 1e-9, "too large": 1e300} {
			if err := c.LLMStatsPriceSet(ctx, model, v, 1, nil).Err(); err == nil {
				t.Errorf("PRICE SET with %s accepted", name)
			}
		}
		if dr, _ := c.LLMStatsDrop(ctx, b).Result(); !dr {
			t.Error("LLMSTATS.DROP = false")
		}
	})
}

func ptr(f float64) *float64 { return &f }

// TestLiveLLMBudgetFloats checks that every float spelling the SDK produces
// is accepted by the server's two different float parsers.
func TestLiveLLMBudgetFloats(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		limits := []struct {
			limit float64
			want  float64
		}{
			{1e-5, 0.00001},      // strict fixed-point: no exponent allowed
			{0.000001, 0.000001}, // the smallest representable budget
			{12.5, 12.5},
			{1e7, 10000000},
			{0, 0}, // 0 = no cost limit
		}
		for i, l := range limits {
			name := k("budget") + string(rune('a'+i))
			ck(t, c.LLMBudgetSet(ctx, name, 1000, l.limit, time.Hour).Err(), "LLMBUDGET.SET")
			st, err := c.LLMBudgetStatus(ctx, name).Result()
			ck(t, err, "LLMBUDGET.STATUS")
			near(t, "cost limit for "+name, st.CostLimitUSD, l.want, 1e-9)
			eq(t, "token limit", st.TokenLimit, int64(1000))
			eq(t, "window", st.WindowSeconds, int64(3600))
			c.LLMBudgetDrop(ctx, name)
		}
		// values that the strict parser would reject are refused client side
		for name, v := range map[string]float64{"1e-12": 1e-12, "negative": -0.5, "NaN": math.NaN(), "Inf": math.Inf(1), "1e300": 1e300} {
			if err := c.LLMBudgetSet(ctx, k("never"), 1000, v, time.Hour).Err(); err == nil {
				t.Errorf("LLMBUDGET.SET with cost %s accepted", name)
			}
		}
		// CONSUME's cost uses the general parser: plain decimals of any size
		name := k("consume")
		ck(t, c.LLMBudgetSet(ctx, name, 1000000, 100, time.Hour).Err(), "SET")
		defer c.LLMBudgetDrop(ctx, name)
		for _, cost := range []float64{1e-5, 1e-12, 0.5, 3, 0} {
			ok, err := c.LLMBudgetConsume(ctx, name, 1, cost, nil).Result()
			ck(t, err, "LLMBUDGET.CONSUME")
			if !ok {
				t.Errorf("CONSUME cost %v rejected", cost)
			}
		}
		st, _ := c.LLMBudgetStatus(ctx, name).Result()
		near(t, "cost used", st.CostUsedUSD, 3.50001, 1e-5)
		eq(t, "tokens used", st.TokensUsed, int64(5))
		eq(t, "requests allowed", st.RequestsAllowed, int64(5))
	})
}

func TestLiveLLMBudget(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		name := k("llmbudget")
		ck(t, c.LLMBudgetSet(ctx, name, 100, 1.5, time.Hour).Err(), "LLMBUDGET.SET")
		defer c.LLMBudgetDrop(ctx, name)
		ok, err := c.LLMBudgetConsume(ctx, name, 60, 0.5, nil).Result()
		ck(t, err, "CONSUME")
		eq(t, "first consume", ok, true)
		ok, _ = c.LLMBudgetConsume(ctx, name, 60, 0.5, nil).Result()
		eq(t, "consume over the token limit", ok, false)
		ok, _ = c.LLMBudgetConsume(ctx, name, 10, 2, nil).Result()
		eq(t, "consume over the cost limit", ok, false)
		ok, _ = c.LLMBudgetConsume(ctx, name, 10, 0.25, nil).Result()
		eq(t, "consume within both limits", ok, true)
		st, err := c.LLMBudgetStatus(ctx, name).Result()
		ck(t, err, "STATUS")
		eq(t, "tokens used", st.TokensUsed, int64(70))
		eq(t, "token limit", st.TokenLimit, int64(100))
		near(t, "cost used", st.CostUsedUSD, 0.75, 1e-9)
		near(t, "cost limit", st.CostLimitUSD, 1.5, 1e-9)
		eq(t, "requests allowed", st.RequestsAllowed, int64(2))
		eq(t, "requests rejected", st.RequestsRejected, int64(2))
		if st.WindowRemaining <= 0 || st.WindowRemaining > 3600 {
			t.Errorf("window remaining = %d", st.WindowRemaining)
		}
		// usage-based consume: priced from the model's table entry
		model := k("budget-model")
		ck(t, c.LLMStatsPriceSet(ctx, model, 10, 20, nil).Err(), "PRICE SET")
		defer c.LLMStatsPriceDel(ctx, model)
		b2 := k("llmbudget2")
		ck(t, c.LLMBudgetSet(ctx, b2, 1000000, 100, time.Hour).Err(), "SET 2")
		defer c.LLMBudgetDrop(ctx, b2)
		ok, err = c.LLMBudgetConsume(ctx, b2, 0, 0, &hotkv.Usage{Model: model, Input: 1000, Output: 500}).Result()
		ck(t, err, "CONSUME usage")
		eq(t, "consume with usage", ok, true)
		st, _ = c.LLMBudgetStatus(ctx, b2).Result()
		eq(t, "tokens used from usage", st.TokensUsed, int64(1500))
		near(t, "cost from usage", st.CostUsedUSD, 0.02, 1e-9) // 1000*10/1M + 500*20/1M

		list, _ := c.LLMBudgetList(ctx).Result()
		if !has(list, name) || !has(list, b2) {
			t.Errorf("LLMBUDGET.LIST = %v", list)
		}
		rs, _ := c.LLMBudgetReset(ctx, name).Result()
		eq(t, "RESET", rs, true)
		st, _ = c.LLMBudgetStatus(ctx, name).Result()
		eq(t, "tokens after RESET", st.TokensUsed, int64(0))
		dr, _ := c.LLMBudgetDrop(ctx, name).Result()
		eq(t, "DROP", dr, true)
		dr, _ = c.LLMBudgetDrop(ctx, name).Result()
		eq(t, "DROP again", dr, false)
	})
}

func TestLivePromptRegistry(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		name := k("prompt")
		defer c.PromptDrop(ctx, name)
		v1, err := c.PromptPut(ctx, name, "hello {{name}}", "first").Result()
		ck(t, err, "PROMPT.PUT")
		eq(t, "first version", v1, int64(1))
		v2, _ := c.PromptPut(ctx, name, "hello again", "").Result()
		eq(t, "second version", v2, int64(2))
		got, err := c.PromptGet(ctx, name, 1).Result()
		ck(t, err, "PROMPT.GET")
		fl := flat(got...)
		if len(fl) < 3 || fl[0] != "1" || fl[1] != "hello {{name}}" || fl[2] != "first" {
			t.Errorf("PROMPT.GET 1 = %v", fl)
		}
		latest, _ := c.PromptLatest(ctx, name).Result()
		fl = flat(latest...)
		if len(fl) < 2 || fl[0] != "2" || fl[1] != "hello again" {
			t.Errorf("PROMPT.LATEST = %v", fl)
		}
		vers, err := c.PromptVersions(ctx, name).Result()
		ck(t, err, "PROMPT.VERSIONS")
		eq(t, "PROMPT.VERSIONS", flat(vers...), []string{"1", "2"})
		ck(t, c.PromptSetActive(ctx, name, 1).Err(), "PROMPT.SETACTIVE")
		list, _ := c.PromptList(ctx).Result()
		if !has(list, name) {
			t.Errorf("PROMPT.LIST = %v", list)
		}
		if err := c.PromptSetActive(ctx, name, 99).Err(); err == nil {
			t.Error("PROMPT.SETACTIVE of an unknown version accepted")
		}
		dr, _ := c.PromptDrop(ctx, name).Result()
		eq(t, "PROMPT.DROP", dr, true)
	})
}

func TestLiveEmbedDedup(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		ns := k("embed")
		defer c.EmbedDedupDrop(ctx, ns)
		fresh, err := c.EmbedDedupPut(ctx, ns, "h1", "modelA", "0.1,0.2,1e-5").Result()
		ck(t, err, "EMBED.DEDUP.PUT")
		eq(t, "first put stores", fresh, true)
		fresh, _ = c.EmbedDedupPut(ctx, ns, "h1", "modelA", "0.1,0.2,1e-5").Result()
		eq(t, "second put is a duplicate", fresh, false)
		v, err := c.EmbedDedupGet(ctx, ns, "h1").Result()
		ck(t, err, "EMBED.DEDUP.GET")
		eq(t, "EMBED.DEDUP.GET", v, "0.1,0.2,1e-5")
		_, err = c.EmbedDedupGet(ctx, ns, "none").Result()
		isNil(t, "EMBED.DEDUP.GET miss", err)
		st, err := c.EmbedDedupStats(ctx, ns).Result()
		ck(t, err, "EMBED.DEDUP.STATS")
		fl := flat(st...)
		if e, _ := field(fl, "entries"); e != "1" {
			t.Errorf("EMBED.DEDUP.STATS = %v", fl)
		}
		list, _ := c.EmbedDedupList(ctx).Result()
		if !has(list, ns) {
			t.Errorf("EMBED.DEDUP.LIST = %v", list)
		}
		rel, _ := c.EmbedDedupRelease(ctx, ns, "h1").Result()
		eq(t, "EMBED.DEDUP.RELEASE", rel, true)
		pg, _ := c.EmbedDedupPurge(ctx, ns).Result()
		_ = pg
		dr, _ := c.EmbedDedupDrop(ctx, ns).Result()
		eq(t, "EMBED.DEDUP.DROP", dr, true)
	})
}

func TestLiveAgentMemory(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		sess, agent := k("sess"), k("agent")
		vec := []float32{0.1, 0.2, 0.3}

		// conversation memory
		ck(t, c.AmemConvCreate(ctx, sess, 10, time.Hour).Err(), "AMEM.CONV.CREATE")
		defer c.AmemConvDrop(ctx, sess)
		ck(t, c.AmemConvAppend(ctx, sess, "user", "hi").Err(), "AMEM.CONV.APPEND")
		ck(t, c.AmemConvAppend(ctx, sess, "assistant", "hello there").Err(), "AMEM.CONV.APPEND 2")
		conv, err := c.AmemConvGet(ctx, sess).Result()
		ck(t, err, "AMEM.CONV.GET")
		eq(t, "AMEM.CONV.GET", flat(conv...), []string{"role", "user", "content", "hi", "role", "assistant", "content", "hello there"})
		win, _ := c.AmemConvWindow(ctx, sess, 1).Result()
		eq(t, "AMEM.CONV.WINDOW 1", flat(win...), []string{"role", "assistant", "content", "hello there"})
		cl, _ := c.AmemConvClear(ctx, sess).Result()
		eq(t, "AMEM.CONV.CLEAR", cl, true)
		conv, _ = c.AmemConvGet(ctx, sess).Result()
		eq(t, "AMEM.CONV.GET after clear", len(flat(conv...)), 0)

		// semantic memory
		ck(t, c.AmemSemCreate(ctx, agent, 3).Err(), "AMEM.SEM.CREATE")
		ck(t, c.AmemSemStore(ctx, agent, "k1", "fact one", vec, 0).Err(), "AMEM.SEM.STORE")
		ck(t, c.AmemSemStore(ctx, agent, "k2", "fact two", []float32{0.9, 0, 0}, time.Hour).Err(), "AMEM.SEM.STORE 2")
		rec, err := c.AmemSemRecall(ctx, agent, 1, vec).Result()
		ck(t, err, "AMEM.SEM.RECALL")
		fl := flat(rec...)
		if kv, _ := field(fl, "key"); kv != "k1" {
			t.Errorf("AMEM.SEM.RECALL = %v, want k1", fl)
		}
		if ct, _ := field(fl, "content"); ct != "fact one" {
			t.Errorf("AMEM.SEM.RECALL content = %q", ct)
		}
		keys, _ := c.AmemSemList(ctx, agent).Result()
		eqUnordered(t, "AMEM.SEM.LIST", keys, []string{"k1", "k2"})
		srch, err := c.AmemSearch(ctx, agent, 2, vec).Result()
		ck(t, err, "AMEM.SEARCH")
		if sf := flat(srch...); !has(sf, "k1") || !has(sf, "semantic") {
			t.Errorf("AMEM.SEARCH = %v", sf)
		}
		fg, _ := c.AmemSemForget(ctx, agent, "k2").Result()
		eq(t, "AMEM.SEM.FORGET", fg, true)
		fg, _ = c.AmemSemForget(ctx, agent, "k2").Result()
		eq(t, "AMEM.SEM.FORGET again", fg, false)

		// working memory
		ck(t, c.AmemWorkSet(ctx, sess, "wk", "wv", 0).Err(), "AMEM.WORK.SET")
		ck(t, c.AmemWorkSet(ctx, sess, "wk2", "wv2", time.Hour).Err(), "AMEM.WORK.SET 2")
		wv, err := c.AmemWorkGet(ctx, sess, "wk").Result()
		ck(t, err, "AMEM.WORK.GET")
		eq(t, "AMEM.WORK.GET", wv, "wv")
		_, err = c.AmemWorkGet(ctx, sess, "none").Result()
		isNil(t, "AMEM.WORK.GET miss", err)
		wl, _ := c.AmemWorkList(ctx, sess).Result()
		eqUnordered(t, "AMEM.WORK.LIST", wl, []string{"wk", "wk2"})
		wd, _ := c.AmemWorkDel(ctx, sess, "wk2").Result()
		eq(t, "AMEM.WORK.DEL", wd, true)
		wc, _ := c.AmemWorkClear(ctx, sess).Result()
		eq(t, "AMEM.WORK.CLEAR", wc, true)

		// episodic memory
		ck(t, c.AmemEpisLog(ctx, agent, "note", "first event", "m1").Err(), "AMEM.EPIS.LOG")
		ck(t, c.AmemEpisLog(ctx, agent, "action", "second event", "").Err(), "AMEM.EPIS.LOG 2")
		ev, err := c.AmemEpisQuery(ctx, agent, nil).Result()
		ck(t, err, "AMEM.EPIS.QUERY")
		efl := flat(ev...)
		if !has(efl, "first event") || !has(efl, "second event") {
			t.Errorf("AMEM.EPIS.QUERY = %v", efl)
		}
		lim := uint32(1)
		ev, _ = c.AmemEpisQuery(ctx, agent, &hotkv.AmemEpisQueryOptions{Type: "action", Limit: &lim}).Result()
		efl = flat(ev...)
		if !has(efl, "second event") || has(efl, "first event") {
			t.Errorf("AMEM.EPIS.QUERY TYPE action LIMIT 1 = %v", efl)
		}
		es, err := c.AmemEpisStats(ctx, agent).Result()
		ck(t, err, "AMEM.EPIS.STATS")
		if n, _ := field(flat(es...), "total_events"); n != "2" {
			t.Errorf("AMEM.EPIS.STATS = %v", flat(es...))
		}
		ec, _ := c.AmemEpisClear(ctx, agent).Result()
		eq(t, "AMEM.EPIS.CLEAR", ec, true)

		// summaries
		defer c.AmemSummaryDrop(ctx, agent)
		sv, err := c.AmemSummarySet(ctx, agent, "sum v1").Result()
		ck(t, err, "AMEM.SUMMARY.SET")
		eq(t, "SUMMARY.SET version", sv, int64(1))
		sv, _ = c.AmemSummaryAppend(ctx, agent, " more").Result()
		eq(t, "SUMMARY.APPEND version", sv, int64(2))
		sg, err := c.AmemSummaryGet(ctx, agent).Result()
		ck(t, err, "AMEM.SUMMARY.GET")
		if sf := flat(sg...); len(sf) < 2 || sf[0] != "sum v1\n more" || sf[1] != "2" { // APPEND joins with a newline
			t.Errorf("AMEM.SUMMARY.GET = %v", sf)
		}
		sl, _ := c.AmemSummaryList(ctx).Result()
		if !has(sl, agent) {
			t.Errorf("AMEM.SUMMARY.LIST = %v", sl)
		}
		sd, _ := c.AmemSummaryDrop(ctx, agent).Result()
		eq(t, "AMEM.SUMMARY.DROP", sd, true)
	})
}

func TestLiveRAG(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		p := k("rag")
		ck(t, c.RAGCreate(ctx, p, 3, &hotkv.RAGCreateOptions{ChunkSize: 100, ChunkOverlap: 10}).Err(), "RAG.CREATE")
		defer c.RAGDrop(ctx, p)
		n, err := c.RAGIngest(ctx, p, "d1", "the quick brown fox", map[string]string{"lang": "en"}, [][]float32{{0.1, 0.2, 0.3}}).Result()
		ck(t, err, "RAG.INGEST")
		eq(t, "chunks ingested", n, int64(1))
		n, _ = c.RAGIngest(ctx, p, "d2", "lorem ipsum dolor", nil, [][]float32{{0.9, 0, 1e-5}}).Result()
		eq(t, "chunks ingested d2", n, int64(1))
		res, err := c.RAGSearch(ctx, p, "fox", &hotkv.RAGSearchOptions{K: 2}).Result()
		ck(t, err, "RAG.SEARCH")
		fl := flat(res...)
		if len(fl) < 6 || fl[0] != "d1:0" || fl[1] != "d1" || fl[2] != "the quick brown fox" {
			t.Errorf("RAG.SEARCH = %v", fl)
		}
		res, err = c.RAGSearch(ctx, p, "fox", &hotkv.RAGSearchOptions{K: 1, Alpha: ptr(0.5), Vector: []float32{0.1, 0.2, 0.3}}).Result()
		ck(t, err, "RAG.SEARCH hybrid")
		fl = flat(res...)
		if len(fl) < 6 || fl[1] != "d1" {
			t.Errorf("RAG.SEARCH hybrid = %v", fl)
		}
		res, _ = c.RAGSearch(ctx, p, "fox", &hotkv.RAGSearchOptions{K: 5, Filter: map[string]string{"lang": "fr"}}).Result()
		eq(t, "filtered search finds nothing", len(res), 0)
		st, err := c.RAGStats(ctx, p).Result()
		ck(t, err, "RAG.STATS")
		sf := flat(st...)
		if v, _ := field(sf, "totalDocs"); v != "2" {
			t.Errorf("RAG.STATS = %v", sf)
		}
		list, _ := c.RAGList(ctx).Result()
		if !has(list, p) {
			t.Errorf("RAG.LIST = %v", list)
		}
		ck(t, c.RAGDrop(ctx, p).Err(), "RAG.DROP")
	})
}

func TestLiveRateLimit(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		name := k("rl")
		ck(t, c.RateLimitCreate(ctx, name, 2, 5*time.Second, nil).Err(), "RATELIMIT.CREATE")
		defer c.RateLimitDrop(ctx, name)
		r1, err := c.RateLimitCheck(ctx, name, "a", 0).Result()
		ck(t, err, "RATELIMIT.CHECK")
		eq(t, "first allowed", r1.Allowed, true)
		eq(t, "limit", r1.Limit, int64(2))
		eq(t, "remaining", r1.Remaining, int64(1))
		r2, _ := c.RateLimitCheck(ctx, name, "a", 1).Result()
		eq(t, "second allowed", r2.Allowed, true)
		r3, _ := c.RateLimitCheck(ctx, name, "a", 1).Result()
		eq(t, "third denied", r3.Allowed, false)
		if r3.RetryAfterMs <= 0 || r3.RetryAfterMs > 5000 {
			t.Errorf("retry after = %d", r3.RetryAfterMs)
		}
		pk, err := c.RateLimitPeek(ctx, name, "a").Result()
		ck(t, err, "RATELIMIT.PEEK")
		eq(t, "peek does not consume", pk.Allowed, false)
		other, _ := c.RateLimitCheck(ctx, name, "b", 0).Result()
		eq(t, "other key independent", other.Allowed, true)
		st, err := c.RateLimitStats(ctx, name).Result()
		ck(t, err, "RATELIMIT.STATS")
		sf := flat(st...)
		if v, _ := field(sf, "total_denied"); v != "1" {
			t.Errorf("RATELIMIT.STATS = %v", sf)
		}
		list, _ := c.RateLimitList(ctx).Result()
		if !has(list, name) {
			t.Errorf("RATELIMIT.LIST = %v", list)
		}
		ck(t, c.RateLimitReset(ctx, name, "a").Err(), "RATELIMIT.RESET")
		r4, _ := c.RateLimitCheck(ctx, name, "a", 0).Result()
		eq(t, "allowed after reset", r4.Allowed, true)
		ck(t, c.RateLimitDrop(ctx, name).Err(), "RATELIMIT.DROP")

		// token bucket with fractional refill (general float rule)
		tb := k("rl-tb")
		ck(t, c.RateLimitCreate(ctx, tb, 5, time.Second, &hotkv.RateLimitCreateOptions{Algo: "token_bucket", Refill: 1e-5}).Err(), "CREATE token_bucket")
		defer c.RateLimitDrop(ctx, tb)
		ck(t, c.RateLimitDrop(ctx, tb).Err(), "DROP token_bucket")
		lb := k("rl-lb")
		ck(t, c.RateLimitCreate(ctx, lb, 5, time.Second, &hotkv.RateLimitCreateOptions{Algo: "leaky_bucket", Drain: 2.5}).Err(), "CREATE leaky_bucket")
		c.RateLimitDrop(ctx, lb)
	})
}

func TestLiveTimeSeries(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		ts := k("ts")
		ck(t, c.TSCreate(ctx, ts, 0, map[string]string{"sensor": k("s1")}).Err(), "TS.CREATE")
		defer c.TSDelete(ctx, ts)
		for i, v := range []float64{1.5, 2.5, 3.5, -1e-5} {
			at := int64((i + 1) * 1000)
			got, err := c.TSAdd(ctx, ts, &at, v).Result()
			ck(t, err, "TS.ADD")
			eq(t, "TS.ADD timestamp", got, at)
		}
		auto, err := c.TSAdd(ctx, ts, nil, 9).Result()
		ck(t, err, "TS.ADD *")
		if d := time.Since(time.UnixMilli(auto)); d < -time.Minute || d > time.Minute {
			t.Errorf("TS.ADD * timestamp %d", auto)
		}
		rng, err := c.TSRange(ctx, ts, 0, 4500, 0).Result()
		ck(t, err, "TS.RANGE")
		eqNumbers(t, "TS.RANGE", flat(rng...), []float64{1000, 1.5, 2000, 2.5, 3000, 3.5, 4000, -0.00001})
		rng, _ = c.TSRange(ctx, ts, 0, 4500, 2).Result()
		eq(t, "TS.RANGE COUNT 2", len(flat(rng...)), 4)
		agg, err := c.TSAgg(ctx, ts, 0, 3500, 2*time.Second, "AVG").Result()
		ck(t, err, "TS.AGG")
		eqNumbers(t, "TS.AGG avg", flat(agg...), []float64{0, 1.5, 2000, 3})
		info, err := c.TSInfo(ctx, ts).Result()
		ck(t, err, "TS.INFO")
		if n, _ := field(flat(info...), "totalSamples"); n != "5" {
			t.Errorf("TS.INFO = %v", flat(info...))
		}
		ck(t, c.TSRetention(ctx, ts, time.Hour).Err(), "TS.RETENTION")
		info, _ = c.TSInfo(ctx, ts).Result()
		if n, _ := field(flat(info...), "retentionMs"); n != "3600000" {
			t.Errorf("TS.INFO retention = %v", flat(info...))
		}
		dst := k("ts-down")
		ck(t, c.TSCreate(ctx, dst, 0, nil).Err(), "TS.CREATE dest")
		defer c.TSDelete(ctx, dst)
		ck(t, c.TSDownsample(ctx, ts, dst, "AVG", 2*time.Second).Err(), "TS.DOWNSAMPLE")
		list, err := c.TSList(ctx, "", "").Result()
		ck(t, err, "TS.LIST")
		if !has(list, ts) || !has(list, dst) {
			t.Errorf("TS.LIST = %v", list)
		}
		list, err = c.TSList(ctx, "sensor", k("s1")).Result()
		ck(t, err, "TS.LIST LABEL")
		eq(t, "TS.LIST by label", list, []string{ts})
		ck(t, c.TSDelete(ctx, ts).Err(), "TS.DELETE")
		if err := c.TSInfo(ctx, ts).Err(); err == nil {
			t.Error("TS.INFO of a deleted series must fail")
		}
	})
}

func TestLiveFeatureStore(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		g := k("fg")
		ck(t, c.FeatureCreate(ctx, g, "test group", map[string]string{"score": "int64", "ratio": "double", "name": "string"}).Err(), "FEATURE.CREATE")
		defer c.FeatureDrop(ctx, g)
		ck(t, c.FeatureSet(ctx, g, "e1", map[string]string{"score": "42", "ratio": "0.00001", "name": "bob"}).Err(), "FEATURE.SET")
		ck(t, c.FeatureSet(ctx, g, "e2", map[string]string{"score": "7"}).Err(), "FEATURE.SET 2")
		got, err := c.FeatureGet(ctx, g, "e1").Result()
		ck(t, err, "FEATURE.GET")
		gm := pairMap(flat(got...))
		if gm["score"] != "42" || gm["name"] != "bob" || gm["ratio"] == "" {
			t.Errorf("FEATURE.GET = %v", gm)
		}
		got, _ = c.FeatureGet(ctx, g, "e1", "score").Result()
		one := pairMap(flat(got...))
		if one["score"] != "42" || one["name"] != "" {
			t.Errorf("FEATURE.GET score = %v", one) // the reply also carries a timestamp and a version after the pairs
		}
		batch, err := c.FeatureBatch(ctx, g, []string{"e1", "e2", "nobody"}, "score").Result()
		ck(t, err, "FEATURE.BATCH")
		eq(t, "FEATURE.BATCH entries", len(batch), 3)
		if f := flat(batch[0]); !has(f, "42") {
			t.Errorf("FEATURE.BATCH e1 = %v", f)
		}
		if f := flat(batch[1]); !has(f, "7") {
			t.Errorf("FEATURE.BATCH e2 = %v", f)
		}
		sch, err := c.FeatureSchema(ctx, g).Result()
		ck(t, err, "FEATURE.SCHEMA")
		eq(t, "FEATURE.SCHEMA", pairMap(flat(sch...)), map[string]string{"score": "int64", "ratio": "double", "name": "string"})
		list, _ := c.FeatureList(ctx).Result()
		if !has(list, g) {
			t.Errorf("FEATURE.LIST = %v", list)
		}
		st, err := c.FeatureStats(ctx, g).Result()
		ck(t, err, "FEATURE.STATS")
		if v, _ := field(flat(st...), "entities"); v != "2" {
			t.Errorf("FEATURE.STATS = %v", flat(st...))
		}
		ck(t, c.FeatureDrop(ctx, g).Err(), "FEATURE.DROP")
		if err := c.FeatureSchema(ctx, g).Err(); err == nil {
			t.Error("FEATURE.SCHEMA of a dropped group must fail")
		}
	})
}

func eqNumbers(t *testing.T, what string, got []string, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %q, want %v", what, got, want)
		return
	}
	for i, g := range got {
		f, err := strconv.ParseFloat(g, 64)
		if err != nil || math.Abs(f-want[i]) > 1e-9 {
			t.Errorf("%s[%d] = %q, want %v", what, i, g, want[i])
		}
	}
}

func pairMap(fl []string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(fl); i += 2 {
		m[fl[i]] = fl[i+1]
	}
	return m
}

func TestLiveObserve(t *testing.T) {
	forProtocols(t, func(t *testing.T, c *hotkv.Client, k func(string) string) {
		ctx := context.Background()
		oi, err := c.ObserveInfo(ctx).Result()
		ck(t, err, "OBSERVE INFO")
		if v, _ := field(flat(oi...), "uptime_seconds"); v == "" {
			t.Errorf("OBSERVE INFO = %v", flat(oi...))
		}
		om, err := c.ObserveMetrics(ctx).Result()
		ck(t, err, "OBSERVE METRICS")
		if v, _ := field(flat(om...), "total_commands"); v == "" || v == "0" {
			t.Errorf("OBSERVE METRICS = %v", flat(om...))
		}
		ai, err := c.AlertsInfo(ctx).Result()
		ck(t, err, "ALERTS INFO")
		oldFlags := flat(ai...)
		if v, _ := field(oldFlags, "min_severity"); v == "" {
			t.Errorf("ALERTS INFO = %v", oldFlags)
		}
		// configuration round trip on this (private) server, restored after
		oldSev, _ := field(oldFlags, "min_severity")
		ck(t, c.AlertsSetSeverity(ctx, "warning").Err(), "ALERTS SEVERITY")
		ai, _ = c.AlertsInfo(ctx).Result()
		if v, _ := field(flat(ai...), "min_severity"); v != "warning" {
			t.Errorf("min_severity = %q after ALERTS SEVERITY warning", v)
		}
		ck(t, c.AlertsSetSeverity(ctx, oldSev).Err(), "ALERTS SEVERITY restore")
		if os.Getenv("HOTKV_TEST_ALLOW_ALERTS") == "1" {
			ck(t, c.AlertsSetURL(ctx, "https://hooks.example.com/hotkv-go-sdk-test").Err(), "ALERTS URL")
			ck(t, c.AlertsEnable(ctx).Err(), "ALERTS ENABLE")
			ck(t, c.AlertsTest(ctx, "info", "go sdk test").Err(), "ALERTS TEST")
			ck(t, c.AlertsDisable(ctx).Err(), "ALERTS DISABLE")
		}
	})
}
