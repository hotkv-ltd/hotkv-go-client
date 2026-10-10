// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noExp(t *testing.T, what, s string) {
	t.Helper()
	if strings.ContainsAny(s, "eE") && s != "inf" && s != "-inf" {
		t.Errorf("%s = %q: exponent notation must never be sent", what, s)
	}
	if strings.HasPrefix(s, "+") {
		t.Errorf("%s = %q: a leading '+' must never be sent", what, s)
	}
}

func TestFormatFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{3, "3"},
		{-3, "-3"},
		{0, "0"},
		{math.Copysign(0, -1), "0"}, // "-0" is normalised
		{0.5, "0.5"},
		{0.1, "0.1"},
		{1e-5, "0.00001"},
		{1e-12, "0.000000000001"},
		{1e21, "1" + strings.Repeat("0", 21)},
		{1e300, "1" + strings.Repeat("0", 300)},
		{-1e-5, "-0.00001"},
		{123456789.125, "123456789.125"},
		{math.MaxFloat64, ""}, // checked structurally below
	}
	for _, c := range cases {
		got, err := formatFloat(c.in)
		if err != nil {
			t.Errorf("formatFloat(%v): %v", c.in, err)
			continue
		}
		noExp(t, "formatFloat", got)
		if c.want != "" && got != c.want {
			t.Errorf("formatFloat(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// Round trip: the shortest digits must parse back to the same value.
	for _, f := range []float64{1e-5, 0.1 + 0.2, 1.0 / 3.0, 1e21, 1e300, 5e-324, math.MaxFloat64} {
		s, _ := formatFloat(f)
		back, ok := parseFloatStrict(s)
		if !ok || back != f {
			t.Errorf("formatFloat(%v) = %q does not round-trip (%v)", f, s, back)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if s, err := formatFloat(bad); err == nil {
			t.Errorf("formatFloat(%v) = %q, want an error", bad, s)
		}
	}
}

func parseFloatStrict(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func TestFormatFloat32(t *testing.T) {
	cases := []struct {
		in   float32
		want string
	}{
		{0.1, "0.1"},
		{1, "1"},
		{-2.5, "-2.5"},
		{1e-5, "0.00001"},
		{1e-12, "0.000000000001"},
		{3.4028235e38, "340282350000000000000000000000000000000"},
	}
	for _, c := range cases {
		got, err := formatFloat32(c.in)
		if err != nil {
			t.Errorf("formatFloat32(%v): %v", c.in, err)
			continue
		}
		noExp(t, "formatFloat32", got)
		if got != c.want {
			t.Errorf("formatFloat32(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if _, err := formatFloat32(bad); err == nil {
			t.Errorf("formatFloat32(%v) accepted", bad)
		}
	}
}

func TestFormatScore(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1.5, "1.5"},
		{-0.25, "-0.25"},
		{0, "0"},
		{1e-5, "0.00001"},
		{1e21, "1" + strings.Repeat("0", 21)},
		{math.Inf(1), "inf"},
		{math.Inf(-1), "-inf"},
	}
	for _, c := range cases {
		got, err := formatScore(c.in)
		if err != nil || got != c.want {
			t.Errorf("formatScore(%v) = %q, %v; want %q", c.in, got, err, c.want)
		}
		noExp(t, "formatScore", got)
	}
	if _, err := formatScore(math.NaN()); err == nil {
		t.Error("formatScore(NaN) accepted")
	}
}

func TestFormatUSD(t *testing.T) {
	ok := []struct {
		in   float64
		want string
	}{
		{12.5, "12.5"},
		{0.5, "0.5"},
		{3, "3"},
		{0, "0"},
		{1e-5, "0.00001"},
		{0.000001, "0.000001"},
		{1e-6, "0.000001"},
		{1e7, "10000000"},
		{15.000001, "15.000001"},
		{0.1 + 0.2, "0.3"},
		{1.0000004, "1"}, // rounds to 6 digits
	}
	for _, c := range ok {
		got, err := formatUSD(c.in)
		if err != nil {
			t.Errorf("formatUSD(%v): %v", c.in, err)
			continue
		}
		noExp(t, "formatUSD", got)
		if strings.HasPrefix(got, ".") || strings.HasSuffix(got, ".") || strings.HasPrefix(got, "-") {
			t.Errorf("formatUSD(%v) = %q: needs a leading digit, no dangling '.', no sign", c.in, got)
		}
		if got != c.want {
			t.Errorf("formatUSD(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	bad := []float64{
		-1, -0.000001, math.NaN(), math.Inf(1), math.Inf(-1),
		1e-12,  // rounds to 0 micro-dollars: 0 would mean "no limit"
		4e-7,   // likewise
		1e300,  // beyond uint64 micro-dollars
		1.9e13, // 1.9e19 micros > 2^64
	}
	for _, f := range bad {
		if s, err := formatUSD(f); err == nil {
			t.Errorf("formatUSD(%v) = %q, want an error", f, s)
		}
	}
	// The largest representable amount still works.
	if s, err := formatUSD(1.8e13); err != nil || s != "18000000000000" {
		t.Errorf("formatUSD(1.8e13) = %q, %v", s, err)
	}
}

func TestFormatArgTypes(t *testing.T) {
	good := map[interface{}]string{
		"s": "s", int(7): "7", int8(-8): "-8", int16(16): "16", int32(32): "32",
		int64(-64): "-64", uint(1): "1", uint8(8): "8", uint16(16): "16", uint32(32): "32",
		uint64(math.MaxUint64): "18446744073709551615", true: "1", false: "0",
		float32(0.1): "0.1", 1e-5: "0.00001", 2.0: "2",
	}
	for in, want := range good {
		got, err := convertArg(in)
		if err != nil || got != want {
			t.Errorf("convertArg(%#v) = %q, %v; want %q", in, got, err, want)
		}
	}
	if s, err := convertArg([]byte{0, 1, 0xff}); err != nil || s != "\x00\x01\xff" {
		t.Errorf("convertArg([]byte) = %q, %v", s, err)
	}
	if s, err := convertArg(time.Second); err != nil || s != "1s" {
		t.Errorf("convertArg(Duration) = %q, %v", s, err)
	}
	for _, bad := range []interface{}{nil, struct{}{}, []int{1}, map[string]string{}, math.NaN(), math.Inf(1)} {
		if s, err := convertArg(bad); err == nil {
			t.Errorf("convertArg(%#v) = %q, want an error", bad, s)
		}
	}
}

func TestBadArgMarker(t *testing.T) {
	if err := checkArgs([]string{"SET", "k", "v"}); err != nil {
		t.Fatal(err)
	}
	if err := checkArgs([]string{"SET", "k", argFloat(math.NaN())}); err == nil || !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("checkArgs = %v, want a NaN error", err)
	}
	// A user value that merely looks similar is data, not an error.
	if err := checkArgs([]string{"SET", "k", "\x00hotkv-badarg-0000\x00x"}); err != nil {
		t.Fatalf("lookalike value treated as error: %v", err)
	}
}

// --- per command family: exact wire arguments ---

// wireOf runs fn against a fake server and returns the single command it
// sent.
func wireOf(t *testing.T, fn func(ctx context.Context, c *Client)) []string {
	t.Helper()
	cl, args := captureArgs(t, "+OK\r\n")
	fn(context.Background(), cl)
	return *args
}

// rejected asserts fn fails without sending anything.
func rejected(t *testing.T, name string, fn func(ctx context.Context, c *Client) error) {
	t.Helper()
	fs := newFakeServer(t)
	var n int32
	fs.serve(t, func(args []string) string {
		atomic.AddInt32(&n, 1)
		return "+OK\r\n"
	})
	cl := newFakeClient(t, fs)
	err := fn(context.Background(), cl)
	if err == nil {
		t.Errorf("%s: want an error for an unsendable value, got nil", name)
	}
	if got := atomic.LoadInt32(&n); got != 0 {
		t.Errorf("%s: %d command(s) reached the server, want none", name, got)
	}
}

func TestFloatWireByFamily(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		do   func(ctx context.Context, c *Client)
		want []string
	}{
		{"INCRBYFLOAT tiny", func(ctx context.Context, c *Client) { c.IncrByFloat(ctx, "k", 1e-5) }, []string{"INCRBYFLOAT", "k", "0.00001"}},
		{"INCRBYFLOAT huge", func(ctx context.Context, c *Client) { c.IncrByFloat(ctx, "k", 1e21) }, []string{"INCRBYFLOAT", "k", "1" + strings.Repeat("0", 21)}},
		{"INCRBYFLOAT neg", func(ctx context.Context, c *Client) { c.IncrByFloat(ctx, "k", -2.5) }, []string{"INCRBYFLOAT", "k", "-2.5"}},
		{"INCRBYFLOAT integral", func(ctx context.Context, c *Client) { c.IncrByFloat(ctx, "k", 3) }, []string{"INCRBYFLOAT", "k", "3"}},
		{"HINCRBYFLOAT tiny", func(ctx context.Context, c *Client) { c.HIncrByFloat(ctx, "k", "f", 1e-12) }, []string{"HINCRBYFLOAT", "k", "f", "0.000000000001"}},
		{"ZADD tiny", func(ctx context.Context, c *Client) { c.ZAdd(ctx, "z", Z{Score: 1e-5, Member: "m"}) }, []string{"ZADD", "z", "0.00001", "m"}},
		{"ZADD +inf", func(ctx context.Context, c *Client) { c.ZAdd(ctx, "z", Z{Score: math.Inf(1), Member: "m"}) }, []string{"ZADD", "z", "inf", "m"}},
		{"ZADD -inf", func(ctx context.Context, c *Client) { c.ZAdd(ctx, "z", Z{Score: math.Inf(-1), Member: "m"}) }, []string{"ZADD", "z", "-inf", "m"}},
		{"ZADD huge", func(ctx context.Context, c *Client) { c.ZAdd(ctx, "z", Z{Score: 1e300, Member: "m"}) }, []string{"ZADD", "z", "1" + strings.Repeat("0", 300), "m"}},
		{"ZINCRBY neg", func(ctx context.Context, c *Client) { c.ZIncrBy(ctx, "z", -1e-5, "m") }, []string{"ZINCRBY", "z", "-0.00001", "m"}},
		{"ZUNIONSTORE weights", func(ctx context.Context, c *Client) {
			c.ZUnionStore(ctx, "d", &ZStore{Keys: []string{"a", "b"}, Weights: []float64{1e-5, math.Inf(1)}})
		}, []string{"ZUNIONSTORE", "d", "2", "a", "b", "WEIGHTS", "0.00001", "inf"}},
		{"VSIM.SET", func(ctx context.Context, c *Client) { c.VSIMSet(ctx, "ix", "k", []float32{1e-5, -1, 0.1}) }, []string{"VSIM.SET", "ix", "k", "0.00001", "-1", "0.1"}},
		{"SCACHE.CREATE threshold", func(ctx context.Context, c *Client) {
			c.SCacheCreate(ctx, "ns", 3, &SCacheCreateOptions{Threshold: f(1e-5)})
		}, []string{"SCACHE.CREATE", "ns", "3", "THRESHOLD", "0.00001"}},
		{"SCACHE.SET vector", func(ctx context.Context, c *Client) {
			c.SCacheSet(ctx, "ns", "q", "r", []float32{1e-5, 2, 3e-12}, 0, nil)
		}, []string{"SCACHE.SET", "ns", "q", "r", "0.00001", "2", "0.000000000003"}},
		{"SCACHE.GET threshold", func(ctx context.Context, c *Client) {
			c.SCacheGet(ctx, "ns", []float32{0.5}, f(0.000001))
		}, []string{"SCACHE.GET", "ns", "0.5", "THRESHOLD", "0.000001"}},
		{"SCACHE.THRESHOLD", func(ctx context.Context, c *Client) { c.SCacheThreshold(ctx, "ns", 1e-7) }, []string{"SCACHE.THRESHOLD", "ns", "0.0000001"}},
		{"PCACHE.CREATE cost", func(ctx context.Context, c *Client) { c.PCacheCreate(ctx, "ns", 0, 0, 1e-5) }, []string{"PCACHE.CREATE", "ns", "COST_PER_HIT", "0.00001"}},
		{"LLMSTATS.RECORD cost", func(ctx context.Context, c *Client) {
			c.LLMStatsRecord(ctx, "b", 1, 2, 3, 1e-5, time.Second, nil)
		}, []string{"LLMSTATS.RECORD", "b", "1", "2", "3", "0.00001", "1000"}},
		{"LLMSTATS.RECORD negative cost allowed by general rule", func(ctx context.Context, c *Client) {
			c.LLMStatsRecord(ctx, "b", 1, 2, 3, -1.5, 0, nil)
		}, []string{"LLMSTATS.RECORD", "b", "1", "2", "3", "-1.5", "0"}},
		{"LLMSTATS.PRICE SET strict USD", func(ctx context.Context, c *Client) {
			c.LLMStatsPriceSet(ctx, "m", 3, 1e-5, &LLMPriceSetOptions{CacheReadUSDPerMtok: f(0.3), CacheWrite5mUSDPerMtok: f(12.5), CacheWrite1hUSDPerMtok: f(1e7)})
		}, []string{"LLMSTATS.PRICE", "SET", "m", "INPUT", "3", "OUTPUT", "0.00001", "CACHEREAD", "0.3", "CACHEWRITE5M", "12.5", "CACHEWRITE1H", "10000000"}},
		{"LLMBUDGET.SET strict USD tiny", func(ctx context.Context, c *Client) {
			c.LLMBudgetSet(ctx, "bd", 100, 1e-5, time.Hour)
		}, []string{"LLMBUDGET.SET", "bd", "100", "0.00001", "3600"}},
		{"LLMBUDGET.SET strict USD 12.5", func(ctx context.Context, c *Client) {
			c.LLMBudgetSet(ctx, "bd", 100, 12.5, time.Hour)
		}, []string{"LLMBUDGET.SET", "bd", "100", "12.5", "3600"}},
		{"LLMBUDGET.SET strict USD 1e7", func(ctx context.Context, c *Client) {
			c.LLMBudgetSet(ctx, "bd", 100, 1e7, time.Hour)
		}, []string{"LLMBUDGET.SET", "bd", "100", "10000000", "3600"}},
		{"LLMBUDGET.SET zero means no limit", func(ctx context.Context, c *Client) {
			c.LLMBudgetSet(ctx, "bd", 100, 0, time.Hour)
		}, []string{"LLMBUDGET.SET", "bd", "100", "0", "3600"}},
		{"LLMBUDGET.CONSUME general cost", func(ctx context.Context, c *Client) {
			c.LLMBudgetConsume(ctx, "bd", 5, 1e-12, nil)
		}, []string{"LLMBUDGET.CONSUME", "bd", "5", "0.000000000001"}},
		{"RAG.INGEST vectors", func(ctx context.Context, c *Client) {
			c.RAGIngest(ctx, "p", "d", "t", nil, [][]float32{{1e-5, 2}, {3, 4}})
		}, []string{"RAG.INGEST", "p", "d", "t", "VECTORS", "0.00001,2", "3,4"}},
		{"RAG.SEARCH alpha+vector", func(ctx context.Context, c *Client) {
			c.RAGSearch(ctx, "p", "q", &RAGSearchOptions{Alpha: f(1e-5), Vector: []float32{1e-5, 1}})
		}, []string{"RAG.SEARCH", "p", "q", "ALPHA", "0.00001", "VECTOR", "0.00001,1"}},
		{"RATELIMIT.CREATE refill/drain", func(ctx context.Context, c *Client) {
			c.RateLimitCreate(ctx, "rl", 10, time.Second, &RateLimitCreateOptions{Refill: 1e-5, Drain: 2.5})
		}, []string{"RATELIMIT.CREATE", "rl", "10", "1000", "REFILL", "0.00001", "DRAIN", "2.5"}},
		{"TS.ADD value", func(ctx context.Context, c *Client) { c.TSAdd(ctx, "ts", nil, 1e-12) }, []string{"TS.ADD", "ts", "*", "0.000000000001"}},
		{"AMEM.SEM.STORE vector", func(ctx context.Context, c *Client) {
			c.AmemSemStore(ctx, "ag", "k", "c", []float32{1e-5, 1}, 0)
		}, []string{"AMEM.SEM.STORE", "ag", "k", "c", "0.00001", "1"}},
		{"AMEM.SEARCH vector", func(ctx context.Context, c *Client) { c.AmemSearch(ctx, "ag", 3, []float32{1e-5}) }, []string{"AMEM.SEARCH", "ag", "3", "0.00001"}},
		{"Do float args", func(ctx context.Context, c *Client) { c.Do(ctx, "CMS.INITBYPROB", "k", 1e-5, 0.01) }, []string{"CMS.INITBYPROB", "k", "0.00001", "0.01"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := wireOf(t, c.do)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("wire = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFloatRejectedWithoutSending(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		do   func(ctx context.Context, c *Client) error
	}{
		{"IncrByFloat NaN", func(ctx context.Context, c *Client) error { return c.IncrByFloat(ctx, "k", nan).Err() }},
		{"IncrByFloat Inf", func(ctx context.Context, c *Client) error { return c.IncrByFloat(ctx, "k", inf).Err() }},
		{"HIncrByFloat NaN", func(ctx context.Context, c *Client) error { return c.HIncrByFloat(ctx, "k", "f", nan).Err() }},
		{"ZAdd NaN", func(ctx context.Context, c *Client) error { return c.ZAdd(ctx, "z", Z{Score: nan, Member: "m"}).Err() }},
		{"ZIncrBy NaN", func(ctx context.Context, c *Client) error { return c.ZIncrBy(ctx, "z", nan, "m").Err() }},
		{"ZUnionStore NaN weight", func(ctx context.Context, c *Client) error {
			return c.ZUnionStore(ctx, "d", &ZStore{Keys: []string{"a"}, Weights: []float64{nan}}).Err()
		}},
		{"VSIMSet NaN", func(ctx context.Context, c *Client) error {
			return c.VSIMSet(ctx, "i", "k", []float32{float32(nan)}).Err()
		}},
		{"VSIMSet Inf", func(ctx context.Context, c *Client) error {
			return c.VSIMSet(ctx, "i", "k", []float32{float32(inf)}).Err()
		}},
		{"VSIMSearch NaN", func(ctx context.Context, c *Client) error {
			return c.VSIMSearch(ctx, "i", []float32{float32(nan)}, 3, nil).Err()
		}},
		{"SCacheSet NaN", func(ctx context.Context, c *Client) error {
			return c.SCacheSet(ctx, "ns", "q", "r", []float32{float32(nan)}, 0, nil).Err()
		}},
		{"SCacheGet Inf threshold", func(ctx context.Context, c *Client) error {
			return c.SCacheGet(ctx, "ns", []float32{1}, f(inf)).Err()
		}},
		{"SCacheThreshold NaN", func(ctx context.Context, c *Client) error { return c.SCacheThreshold(ctx, "ns", nan).Err() }},
		{"PCacheCreate NaN", func(ctx context.Context, c *Client) error { return c.PCacheCreate(ctx, "ns", 0, 0, nan).Err() }},
		{"LLMStatsRecord Inf", func(ctx context.Context, c *Client) error {
			return c.LLMStatsRecord(ctx, "b", 1, 1, 0, inf, 0, nil).Err()
		}},
		{"LLMBudgetConsume NaN", func(ctx context.Context, c *Client) error { return c.LLMBudgetConsume(ctx, "b", 1, nan, nil).Err() }},
		{"LLMBudgetSet negative", func(ctx context.Context, c *Client) error { return c.LLMBudgetSet(ctx, "b", 1, -1, time.Hour).Err() }},
		{"LLMBudgetSet NaN", func(ctx context.Context, c *Client) error { return c.LLMBudgetSet(ctx, "b", 1, nan, time.Hour).Err() }},
		{"LLMBudgetSet Inf", func(ctx context.Context, c *Client) error { return c.LLMBudgetSet(ctx, "b", 1, inf, time.Hour).Err() }},
		{"LLMBudgetSet rounds to 0", func(ctx context.Context, c *Client) error {
			return c.LLMBudgetSet(ctx, "b", 1, 1e-12, time.Hour).Err()
		}},
		{"LLMBudgetSet too large", func(ctx context.Context, c *Client) error { return c.LLMBudgetSet(ctx, "b", 1, 1e300, time.Hour).Err() }},
		{"LLMStatsPriceSet negative", func(ctx context.Context, c *Client) error {
			return c.LLMStatsPriceSet(ctx, "m", -1, 1, nil).Err()
		}},
		{"LLMStatsPriceSet rounds to 0", func(ctx context.Context, c *Client) error {
			return c.LLMStatsPriceSet(ctx, "m", 1, 1e-9, nil).Err()
		}},
		{"LLMStatsPriceSet cache NaN", func(ctx context.Context, c *Client) error {
			return c.LLMStatsPriceSet(ctx, "m", 1, 1, &LLMPriceSetOptions{CacheReadUSDPerMtok: f(nan)}).Err()
		}},
		{"RAGIngest NaN", func(ctx context.Context, c *Client) error {
			return c.RAGIngest(ctx, "p", "d", "t", nil, [][]float32{{float32(nan)}}).Err()
		}},
		{"RAGSearch NaN alpha", func(ctx context.Context, c *Client) error {
			return c.RAGSearch(ctx, "p", "q", &RAGSearchOptions{Alpha: f(nan)}).Err()
		}},
		{"RateLimitCreate NaN", func(ctx context.Context, c *Client) error {
			return c.RateLimitCreate(ctx, "r", 1, time.Second, &RateLimitCreateOptions{Refill: nan}).Err()
		}},
		{"TSAdd Inf", func(ctx context.Context, c *Client) error { return c.TSAdd(ctx, "ts", nil, inf).Err() }},
		{"AmemSemStore NaN", func(ctx context.Context, c *Client) error {
			return c.AmemSemStore(ctx, "a", "k", "c", []float32{float32(nan)}, 0).Err()
		}},
		{"Set unsupported type", func(ctx context.Context, c *Client) error { return c.Set(ctx, "k", struct{ A int }{1}, 0).Err() }},
		{"Set nil", func(ctx context.Context, c *Client) error { return c.Set(ctx, "k", nil, 0).Err() }},
		{"Do NaN", func(ctx context.Context, c *Client) error { return c.Do(ctx, "X", nan).Err() }},
		{"Pipeline NaN", func(ctx context.Context, c *Client) error {
			p := c.Pipeline()
			p.Do(ctx, "SET", "a", "1")
			p.Do(ctx, "INCRBYFLOAT", "a", nan)
			_, err := p.Exec(ctx)
			return err
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) { rejected(t, c.name, c.do) })
	}
}

func TestCmdTypedAccessors(t *testing.T) {
	cl, _ := captureArgs(t, "*3\r\n$1\r\na\r\n:2\r\n_\r\n")
	cmd := cl.Do(context.Background(), "ANY")
	got, err := cmd.Result()
	if err != nil || !reflect.DeepEqual(got, []interface{}{"a", int64(2), nil}) {
		t.Fatalf("Result = %#v, %v", got, err)
	}
	ss, err := cmd.StringSlice()
	if err != nil || !reflect.DeepEqual(ss, []string{"a", "2", ""}) {
		t.Fatalf("StringSlice = %#v, %v", ss, err)
	}
	if _, err := cmd.Int64(); err == nil {
		t.Fatal("Int64 of an array must fail")
	}
}
