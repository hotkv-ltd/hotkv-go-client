// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

// --- test fixture: a tiny loopback RESP fake, no HotKV server involved ---
//
// fakeServer accepts exactly one connection, replies to the client's HELLO
// handshake with a RESP2 error (so Conn.init falls back to protocol 2 and
// proceeds without AUTH/SELECT, matching the Options these tests use), then
// for every following command on that connection calls handle with the
// decoded argument list and writes back whatever raw RESP bytes handle
// returns. It lets these tests capture exact argument lists and feed
// hand-built canned replies (RESP2 flat arrays or RESP3 maps) through the
// real wire-decode path, without a HotKV server.
type fakeServer struct {
	ln net.Listener
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fs := &fakeServer{ln: ln}
	t.Cleanup(func() { ln.Close() })
	return fs
}

func (fs *fakeServer) addr() string { return fs.ln.Addr().String() }

// serve accepts one connection and answers up to len(handle) commands
// (after the HELLO handshake) by calling handle in order.
func (fs *fakeServer) serve(t *testing.T, handle func(args []string) string) {
	t.Helper()
	go func() {
		conn, err := fs.ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := NewRespReader(conn)

		// HELLO handshake: reply with an error so the client falls back to
		// RESP2 and continues (Options below set no Password/DB).
		if _, err := r.ReadValue(); err != nil {
			return
		}
		if _, err := conn.Write([]byte("-ERR HELLO not supported by test fake\r\n")); err != nil {
			return
		}

		for {
			val, err := r.ReadValue()
			if err != nil {
				return
			}
			args := make([]string, len(val.Elems))
			for i, e := range val.Elems {
				args[i] = e.Str
			}
			if _, err := conn.Write([]byte(handle(args))); err != nil {
				return
			}
		}
	}()
}

func newFakeClient(t *testing.T, fs *fakeServer) *Client {
	t.Helper()
	client, err := NewClient(&Options{
		Addr:        fs.addr(),
		MaxRetries:  0,
		DialTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// captureArgs starts a fake server that captures exactly one command's
// argument list, replies with raw, and returns the client plus a pointer to
// the captured args (filled in once the command under test has been sent).
func captureArgs(t *testing.T, raw string) (*Client, *[]string) {
	t.Helper()
	fs := newFakeServer(t)
	captured := new([]string)
	fs.serve(t, func(args []string) string {
		*captured = args
		return raw
	})
	return newFakeClient(t, fs), captured
}

// --- RESP wire builders for canned replies ---

func respBulk(s string) string     { return "$" + itoa(len(s)) + "\r\n" + s + "\r\n" }
func respInt(n int64) string       { return ":" + itoa64(n) + "\r\n" }
func respArrayHeader(n int) string { return "*" + itoa(n) + "\r\n" }
func respMapHeader(n int) string   { return "%" + itoa(n) + "\r\n" }
func respNullArray() string        { return "*-1\r\n" }

func itoa(n int) string { return itoa64(int64(n)) }
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// pairs renders a flat RESP2 array of alternating name/value bulk strings.
func pairsArray(kv ...string) string {
	s := respArrayHeader(len(kv))
	for _, v := range kv {
		s += respBulk(v)
	}
	return s
}

// pairsArrayInt is pairsArray but with the values at the given (0-based,
// value-index) positions encoded as RESP integers instead of bulk strings.
func pairsArrayMixed(intIdx map[int]bool, kv ...string) string {
	s := respArrayHeader(len(kv))
	for i, v := range kv {
		valueIdx := i / 2
		if i%2 == 1 && intIdx[valueIdx] {
			s += respInt(atoi64(v))
		} else {
			s += respBulk(v)
		}
	}
	return s
}

func atoi64(s string) int64 {
	var n int64
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n
}

// pairsMap renders a RESP3 map of alternating name/value bulk strings, with
// the values at the given (0-based, value-index) positions as integers.
func pairsMap(intIdx map[int]bool, kv ...string) string {
	s := respMapHeader(len(kv) / 2)
	for i, v := range kv {
		valueIdx := i / 2
		if i%2 == 1 && intIdx[valueIdx] {
			s += respInt(atoi64(v))
		} else {
			s += respBulk(v)
		}
	}
	return s
}

// --- Usage / usageArgs: every option and combination ---

func TestUsageArgsOmittedWhenNil(t *testing.T) {
	got := usageArgs([]string{"BASE"}, nil)
	want := []string{"BASE"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("usageArgs(nil) = %v, want %v", got, want)
	}
}

func TestUsageArgsAlwaysSendsModel(t *testing.T) {
	got := usageArgs([]string{"BASE"}, &Usage{})
	want := []string{"BASE", "MODEL", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("usageArgs(empty Usage) = %v, want %v", got, want)
	}
}

func TestUsageArgsFullCombination(t *testing.T) {
	got := usageArgs(nil, &Usage{
		Model: "claude-sonnet-5", Input: 100, Output: 50,
		CacheRead: 10, CacheWrite5m: 20, CacheWrite1h: 30,
	})
	want := []string{
		"MODEL", "claude-sonnet-5",
		"INPUT", "100",
		"OUTPUT", "50",
		"CACHEREAD", "10",
		"CACHEWRITE5M", "20",
		"CACHEWRITE1H", "30",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("usageArgs(full) = %v, want %v", got, want)
	}
}

func TestUsageArgsOmitsZeroCounts(t *testing.T) {
	got := usageArgs(nil, &Usage{Model: "gpt-4", Input: 5})
	want := []string{"MODEL", "gpt-4", "INPUT", "5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("usageArgs(partial) = %v, want %v", got, want)
	}
}

// --- exact argument lists: PCACHE.SET / SCACHE.SET / keyed cache SET ---

func TestPCacheSetArgsNoUsage(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	if err := client.PCacheSet(context.Background(), "ns", "p", "r", 0, "", nil).Err(); err != nil {
		t.Fatalf("PCacheSet: %v", err)
	}
	want := []string{"PCACHE.SET", "ns", "p", "r"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestPCacheSetArgsTTLMetaAndUsage(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	usage := &Usage{Model: "gpt-4", Input: 12, Output: 8, CacheRead: 4}
	err := client.PCacheSet(context.Background(), "ns", "p", "r", 30*time.Second, `{"k":"v"}`, usage).Err()
	if err != nil {
		t.Fatalf("PCacheSet: %v", err)
	}
	want := []string{
		"PCACHE.SET", "ns", "p", "r",
		"TTL", "30",
		"META", `{"k":"v"}`,
		"MODEL", "gpt-4",
		"INPUT", "12",
		"OUTPUT", "8",
		"CACHEREAD", "4",
	}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestSCacheSetArgsWithUsage(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	usage := &Usage{Model: "gpt-4", CacheWrite5m: 7}
	err := client.SCacheSet(context.Background(), "ns", "q", "r", []float32{0.1, 0.2}, 0, usage).Err()
	if err != nil {
		t.Fatalf("SCacheSet: %v", err)
	}
	want := []string{"SCACHE.SET", "ns", "q", "r", "0.1", "0.2", "MODEL", "gpt-4", "CACHEWRITE5M", "7"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestKeyedCacheSetArgsWithUsage(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	usage := &Usage{Model: "gpt-4", CacheWrite1h: 9}
	err := client.ToolCacheSet(context.Background(), "ns", "k", "v", time.Minute, "meta", usage).Err()
	if err != nil {
		t.Fatalf("ToolCacheSet: %v", err)
	}
	want := []string{"TOOL.CACHE.SET", "ns", "k", "v", "TTL", "60", "META", "meta", "MODEL", "gpt-4", "CACHEWRITE1H", "9"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

// --- exact argument lists: LLMSTATS.RECORD options ---

func TestLLMStatsRecordArgsNilOptions(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	err := client.LLMStatsRecord(context.Background(), "bucket", 100, 50, 0, 0.002, 120*time.Millisecond, nil).Err()
	if err != nil {
		t.Fatalf("LLMStatsRecord: %v", err)
	}
	want := []string{"LLMSTATS.RECORD", "bucket", "100", "50", "0", "0.002", "120"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMStatsRecordArgsErrorOnly(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	e := true
	err := client.LLMStatsRecord(context.Background(), "bucket", 1, 2, 3, 0.1, time.Second,
		&LLMStatsRecordOptions{Error: &e}).Err()
	if err != nil {
		t.Fatalf("LLMStatsRecord: %v", err)
	}
	want := []string{"LLMSTATS.RECORD", "bucket", "1", "2", "3", "0.1", "1000", "ERROR", "1"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMStatsRecordArgsModelAndCacheWrites(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	f := false
	err := client.LLMStatsRecord(context.Background(), "bucket", 100, 50, 10, 0, 80*time.Millisecond,
		&LLMStatsRecordOptions{Error: &f, Model: "claude-sonnet-5", CacheWrite5m: 20, CacheWrite1h: 30}).Err()
	if err != nil {
		t.Fatalf("LLMStatsRecord: %v", err)
	}
	want := []string{
		"LLMSTATS.RECORD", "bucket", "100", "50", "10", "0", "80",
		"ERROR", "0", "MODEL", "claude-sonnet-5", "CACHEWRITE5M", "20", "CACHEWRITE1H", "30",
	}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

// --- exact argument lists: LLMSTATS.PRICE SET/GET/LIST/DEL ---

func TestLLMStatsPriceSetArgsRequiredOnly(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	err := client.LLMStatsPriceSet(context.Background(), "my-model", 3, 15, nil).Err()
	if err != nil {
		t.Fatalf("LLMStatsPriceSet: %v", err)
	}
	want := []string{"LLMSTATS.PRICE", "SET", "my-model", "INPUT", "3", "OUTPUT", "15"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMStatsPriceSetArgsAllOverrides(t *testing.T) {
	client, args := captureArgs(t, "+OK\r\n")
	cr, cw5, cw1 := 0.5, 4.0, 6.0
	opts := &LLMPriceSetOptions{CacheReadUSDPerMtok: &cr, CacheWrite5mUSDPerMtok: &cw5, CacheWrite1hUSDPerMtok: &cw1}
	err := client.LLMStatsPriceSet(context.Background(), "my-model", 3, 15, opts).Err()
	if err != nil {
		t.Fatalf("LLMStatsPriceSet: %v", err)
	}
	want := []string{
		"LLMSTATS.PRICE", "SET", "my-model", "INPUT", "3", "OUTPUT", "15",
		"CACHEREAD", "0.5", "CACHEWRITE5M", "4", "CACHEWRITE1H", "6",
	}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMStatsPriceGetArgs(t *testing.T) {
	client, args := captureArgs(t, pairsMap(map[int]bool{2: true, 3: true},
		"model", "gpt-4", "source", "unpriced", "priced", "0", "retired", "0",
		"input_usd_per_mtok", "0.000000", "output_usd_per_mtok", "0.000000",
		"cache_read_usd_per_mtok", "0.000000", "cache_write_5m_usd_per_mtok", "0.000000",
		"cache_write_1h_usd_per_mtok", "0.000000"))
	if _, err := client.LLMStatsPriceGet(context.Background(), "gpt-4").Result(); err != nil {
		t.Fatalf("LLMStatsPriceGet: %v", err)
	}
	want := []string{"LLMSTATS.PRICE", "GET", "gpt-4"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMStatsPriceListArgs(t *testing.T) {
	client, args := captureArgs(t, respArrayHeader(0))
	if _, err := client.LLMStatsPriceList(context.Background()).Result(); err != nil {
		t.Fatalf("LLMStatsPriceList: %v", err)
	}
	want := []string{"LLMSTATS.PRICE", "LIST"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMStatsPriceDelArgs(t *testing.T) {
	client, args := captureArgs(t, respInt(1))
	if _, err := client.LLMStatsPriceDel(context.Background(), "gpt-4").Result(); err != nil {
		t.Fatalf("LLMStatsPriceDel: %v", err)
	}
	want := []string{"LLMSTATS.PRICE", "DEL", "gpt-4"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

// --- exact argument lists: LLMBUDGET.CONSUME ---

func TestLLMBudgetConsumeArgsLegacy(t *testing.T) {
	client, args := captureArgs(t, respInt(1))
	if _, err := client.LLMBudgetConsume(context.Background(), "name", 10, 0.01, nil).Result(); err != nil {
		t.Fatalf("LLMBudgetConsume: %v", err)
	}
	want := []string{"LLMBUDGET.CONSUME", "name", "10", "0.01"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestLLMBudgetConsumeArgsWithUsage(t *testing.T) {
	client, args := captureArgs(t, respInt(1))
	usage := &Usage{Model: "gpt-4", Input: 10, Output: 5}
	if _, err := client.LLMBudgetConsume(context.Background(), "name", 0, 0, usage).Result(); err != nil {
		t.Fatalf("LLMBudgetConsume: %v", err)
	}
	want := []string{"LLMBUDGET.CONSUME", "name", "0", "0", "MODEL", "gpt-4", "INPUT", "10", "OUTPUT", "5"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

// --- canned reply parsing: RESP2 flat arrays and RESP3 maps ---

func priceEntryKV(model, source string, priced, retired bool) []string {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return []string{
		"model", model, "source", source, "priced", b(priced), "retired", b(retired),
		"input_usd_per_mtok", "3.000000", "output_usd_per_mtok", "15.000000",
		"cache_read_usd_per_mtok", "0.300000", "cache_write_5m_usd_per_mtok", "3.750000",
		"cache_write_1h_usd_per_mtok", "6.000000",
	}
}

func priceIntFields() map[int]bool { return map[int]bool{2: true, 3: true} }

func wantPriceResult(model, source string, priced, retired bool) LLMPriceResult {
	return LLMPriceResult{
		Model: model, Source: source, Priced: priced, Retired: retired,
		InputUSDPerMtok: 3, OutputUSDPerMtok: 15, CacheReadUSDPerMtok: 0.3,
		CacheWrite5mUSDPerMtok: 3.75, CacheWrite1hUSDPerMtok: 6,
	}
}

func TestLLMStatsPriceGetParsesRESP2FlatArray(t *testing.T) {
	client, _ := captureArgs(t, pairsArrayMixed(priceIntFields(), priceEntryKV("claude-sonnet-5", "custom", true, false)...))
	got, err := client.LLMStatsPriceGet(context.Background(), "claude-sonnet-5").Result()
	if err != nil {
		t.Fatalf("LLMStatsPriceGet: %v", err)
	}
	want := wantPriceResult("claude-sonnet-5", "custom", true, false)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMStatsPriceGet (RESP2) = %+v, want %+v", got, want)
	}
}

func TestLLMStatsPriceGetParsesRESP3Map(t *testing.T) {
	client, _ := captureArgs(t, pairsMap(priceIntFields(), priceEntryKV("gpt-6-sol", "default", true, false)...))
	got, err := client.LLMStatsPriceGet(context.Background(), "gpt-6-sol").Result()
	if err != nil {
		t.Fatalf("LLMStatsPriceGet: %v", err)
	}
	want := wantPriceResult("gpt-6-sol", "default", true, false)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMStatsPriceGet (RESP3) = %+v, want %+v", got, want)
	}
}

func TestLLMStatsPriceGetUnknownModel(t *testing.T) {
	client, _ := captureArgs(t, pairsMap(priceIntFields(), priceEntryKV("unknown-model", "unpriced", false, false)...))
	got, err := client.LLMStatsPriceGet(context.Background(), "unknown-model").Result()
	if err != nil {
		t.Fatalf("LLMStatsPriceGet: %v", err)
	}
	if got.Source != "unpriced" || got.Priced {
		t.Errorf("LLMStatsPriceGet(unknown) = %+v, want source=unpriced priced=false", got)
	}
}

func TestLLMStatsPriceListParsesMultipleEntries(t *testing.T) {
	e1 := priceEntryKV("model-a", "default", true, false)
	e2 := priceEntryKV("model-b", "custom", true, true)
	raw := respArrayHeader(2) + pairsMap(priceIntFields(), e1...) + pairsMap(priceIntFields(), e2...)
	client, _ := captureArgs(t, raw)
	got, err := client.LLMStatsPriceList(context.Background()).Result()
	if err != nil {
		t.Fatalf("LLMStatsPriceList: %v", err)
	}
	if len(got) != 2 || got[0].Model != "model-a" || got[1].Model != "model-b" || !got[1].Retired {
		t.Errorf("LLMStatsPriceList = %+v, want 2 entries model-a then model-b (b retired)", got)
	}
}

func TestSCacheStatsParsesRESP3Map(t *testing.T) {
	kv := []string{
		"entries", "3", "hits", "10", "misses", "2", "hit_rate", "0.833333", "p50_us", "150",
		"usage_saved_requests", "10", "usage_saved_input_tokens", "120", "usage_saved_output_tokens", "80",
		"usage_saved_cache_read_tokens", "0", "usage_saved_cache_write_tokens", "0",
		"usage_saved_cost_usd", "0.000160",
	}
	intFields := map[int]bool{0: true, 2: true, 4: true, 5: true, 6: true, 7: true, 8: true, 9: true}
	client, _ := captureArgs(t, pairsMap(intFields, kv...))
	got, err := client.SCacheStats(context.Background(), "ns").Result()
	if err != nil {
		t.Fatalf("SCacheStats: %v", err)
	}
	want := SCacheStatsResult{
		Entries: 3, Hits: 10, Misses: 2, HitRate: 0.833333, P50Us: 150,
		Usage: UsageSavings{Requests: 10, InputTokens: 120, OutputTokens: 80, CostUSD: 0.00016},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SCacheStats = %+v, want %+v", got, want)
	}
}

func TestPCacheStatsParsesRESP2FlatArrayMissingFieldsDefaultZero(t *testing.T) {
	// Only a subset of fields present (as if talking to an older/newer
	// server): missing fields must default to zero, not error.
	kv := []string{"entries", "1", "hits", "4"}
	client, _ := captureArgs(t, pairsArrayMixed(map[int]bool{0: true, 1: true}, kv...))
	got, err := client.PCacheStats(context.Background(), "ns").Result()
	if err != nil {
		t.Fatalf("PCacheStats: %v", err)
	}
	if got.Entries != 1 || got.Hits != 4 || got.Misses != 0 || got.CostSavedUSD != 0 {
		t.Errorf("PCacheStats = %+v, want Entries=1 Hits=4 Misses=0 CostSavedUSD=0", got)
	}
}

func TestKeyedCacheStatsParsesRESP3Map(t *testing.T) {
	kv := []string{
		"entries", "7", "hits", "5", "misses", "1", "evictions", "0", "memory_bytes", "2048",
		"usage_saved_requests", "5", "usage_saved_input_tokens", "50", "usage_saved_output_tokens", "10",
		"usage_saved_cache_read_tokens", "0", "usage_saved_cache_write_tokens", "0",
		"usage_saved_cost_usd", "0.000050",
	}
	intFields := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true, 9: true}
	client, _ := captureArgs(t, pairsMap(intFields, kv...))
	got, err := client.ToolCacheStats(context.Background(), "ns").Result()
	if err != nil {
		t.Fatalf("ToolCacheStats: %v", err)
	}
	if got.Entries != 7 || got.MemoryBytes != 2048 || got.Usage.CostUSD != 0.00005 {
		t.Errorf("ToolCacheStats = %+v", got)
	}
}

func TestLLMStatsGetParsesRESP2FlatArray(t *testing.T) {
	kv := []string{
		"requests", "10", "errors", "1", "input_tokens", "500", "output_tokens", "200",
		"cached_tokens", "50", "cost_usd", "0.0123", "avg_latency_ms", "42.5",
		"requests_1m", "3", "tokens_1m", "150", "cost_usd_1m", "0.004",
		"cache_write_5m_tokens", "20", "cache_write_1h_tokens", "0", "unpriced_requests", "2",
	}
	intFields := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 7: true, 8: true, 10: true, 11: true, 12: true}
	client, _ := captureArgs(t, pairsArrayMixed(intFields, kv...))
	got, err := client.LLMStatsGet(context.Background(), "bucket").Result()
	if err != nil {
		t.Fatalf("LLMStatsGet: %v", err)
	}
	want := LLMStatsResult{
		Requests: 10, Errors: 1, InputTokens: 500, OutputTokens: 200, CachedTokens: 50,
		CostUSD: 0.0123, AvgLatencyMs: 42.5, Requests1m: 3, Tokens1m: 150, CostUSD1m: 0.004,
		CacheWrite5mTokens: 20, CacheWrite1hTokens: 0, UnpricedRequests: 2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMStatsGet = %+v, want %+v", got, want)
	}
}

func TestLLMBudgetStatusParsesRESP3Map(t *testing.T) {
	kv := []string{
		"tokens_used", "100", "token_limit", "1000", "cost_used_usd", "0.01",
		"cost_limit_usd", "5.00", "window_seconds", "3600", "window_remaining", "1200",
		"requests_allowed", "9", "requests_rejected", "1", "unpriced_requests", "0",
	}
	intFields := map[int]bool{0: true, 1: true, 4: true, 5: true, 6: true, 7: true, 8: true}
	client, _ := captureArgs(t, pairsMap(intFields, kv...))
	got, err := client.LLMBudgetStatus(context.Background(), "name").Result()
	if err != nil {
		t.Fatalf("LLMBudgetStatus: %v", err)
	}
	want := LLMBudgetStatusResult{
		TokensUsed: 100, TokenLimit: 1000, CostUsedUSD: 0.01, CostLimitUSD: 5.00,
		WindowSeconds: 3600, WindowRemaining: 1200, RequestsAllowed: 9, RequestsRejected: 1,
		UnpricedRequests: 0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMBudgetStatus = %+v, want %+v", got, want)
	}
}

func TestNoLicenseErrorDetectedOnEnterpriseCommands(t *testing.T) {
	client, _ := captureArgs(t, "-NOLICENSE promptcache requires a HotKV license. Contact sales@hotkv.com\r\n")
	err := client.PCacheSet(context.Background(), "ns", "p", "r", 0, "", nil).Err()
	if !IsNoLicenseError(err) {
		t.Errorf("IsNoLicenseError(%v) = false, want true", err)
	}
}
