// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// This file implements the HotKV enterprise and AI command families: GCRA,
// VSIM persistence, SCACHE, PCACHE, the keyed
// caches (TOOL.CACHE/NCACHE/GUARD/RERANK), LLMSTATS, LLMBUDGET, PROMPT,
// EMBED.DEDUP, AMEM (including AMEM.SUMMARY), RAG, RATELIMIT, TS, FEATURE and
// OBSERVE/ALERTS.
//
// Replies without an existing typed Cmd (most STATS/INFO/QUERY commands)
// are returned as *SliceCmd, the same raw-Value escape hatch this package
// already uses for LATENCY LATEST/HISTORY: the server's flat "name, value,
// name, value, ..." pairs come back as val.Elems[i].Str / .Int / .Dbl in
// order.
//
// Duration inputs are always a time.Duration and are converted to whatever
// unit the wire command uses (seconds or milliseconds); zero means "omit
// the option" wherever the server treats the option that way.

func vecToCSV(vec []float32) string {
	parts := make([]string, len(vec))
	for i, f := range vec {
		parts[i] = argFloat32(f)
	}
	return strings.Join(parts, ",")
}

func vecArgs(vec []float32) []string {
	args := make([]string, len(vec))
	for i, f := range vec {
		args[i] = argFloat32(f)
	}
	return args
}

func valInt(v Value) int64 {
	if v.Typ == RespInteger {
		return v.Int
	}
	n, _ := strconv.ParseInt(v.Str, 10, 64)
	return n
}

// Usage records one original (uncached) LLM call's token usage, attached to
// a cache SET (PCACHE.SET, SCACHE.SET, every keyed cache SET) or to
// LLMBUDGET.CONSUME as the shared "token usage group". Model is required whenever a Usage
// is given; the token counts default to 0 and are omitted from the wire when
// zero, but MODEL is always sent so the server can price the call and, on a
// later cache hit, attribute the tokens (and cost) that hit did not spend.
//
// Input/Output are uncached prompt/output tokens, CacheRead is provider
// prompt-cache hit tokens, and CacheWrite5m/CacheWrite1h are Anthropic
// cache-write tokens by TTL tier (leave both 0 for providers without a
// separate cache-write cost, e.g. OpenAI).
type Usage struct {
	Model        string
	Input        uint64
	Output       uint64
	CacheRead    uint64
	CacheWrite5m uint64
	CacheWrite1h uint64
}

// usageArgs appends usage's wire options to args. usage nil omits the group
// entirely (the command behaves exactly as if Usage did not exist); a
// non-nil usage always sends MODEL (required, even if usage.Model happens
// to be empty) followed by only the non-zero token counts.
func usageArgs(args []string, usage *Usage) []string {
	if usage == nil {
		return args
	}
	args = append(args, "MODEL", usage.Model)
	if usage.Input > 0 {
		args = append(args, "INPUT", strconv.FormatUint(usage.Input, 10))
	}
	if usage.Output > 0 {
		args = append(args, "OUTPUT", strconv.FormatUint(usage.Output, 10))
	}
	if usage.CacheRead > 0 {
		args = append(args, "CACHEREAD", strconv.FormatUint(usage.CacheRead, 10))
	}
	if usage.CacheWrite5m > 0 {
		args = append(args, "CACHEWRITE5M", strconv.FormatUint(usage.CacheWrite5m, 10))
	}
	if usage.CacheWrite1h > 0 {
		args = append(args, "CACHEWRITE1H", strconv.FormatUint(usage.CacheWrite1h, 10))
	}
	return args
}

// --- GCRA.* (rate limiter primitive) ---

// GCRALimit implements the Generic Cell Rate Algorithm. count <= 0 defaults
// to 1 token on the server.
func (c *Client) GCRALimit(ctx context.Context, key string, period time.Duration, burst, count int64) *GCRACmd {
	cmd := NewGCRACmd(nil)
	args := []string{"GCRA.LIMIT", key, strconv.FormatInt(period.Milliseconds(), 10), strconv.FormatInt(burst, 10)}
	if count > 0 {
		args = append(args, strconv.FormatInt(count, 10))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	if len(val.Elems) >= 5 {
		cmd.SetResult(GCRAResult{
			Denied:       valInt(val.Elems[0]) != 0,
			Limit:        valInt(val.Elems[1]),
			Remaining:    valInt(val.Elems[2]),
			RetryAfterMs: valInt(val.Elems[3]),
			ResetAfterMs: valInt(val.Elems[4]),
		})
	}
	return cmd
}

// GCRAReset resets rate limit state for the given keys, returning how many
// had state.
func (c *Client) GCRAReset(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := append([]string{"GCRA.RESET"}, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// --- VSIM persistence (needs a license that includes the VSIM persistence feature) ---

// VSIMSave persists an opaque snapshot blob under name, returning bytes written.
func (c *Client) VSIMSave(ctx context.Context, name string, blob []byte) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.SAVE", name, string(blob))
	return cmd
}

// VSIMLoad returns a previously saved snapshot blob.
func (c *Client) VSIMLoad(ctx context.Context, name string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.LOAD", name)
	return cmd
}

// VSIMSnapshots lists saved snapshot names.
func (c *Client) VSIMSnapshots(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.LIST")
	return cmd
}

// VSIMSnapshotDrop deletes a saved snapshot.
func (c *Client) VSIMSnapshotDrop(ctx context.Context, name string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.DROP", name)
	return cmd
}

// --- SCACHE.* (semantic cache, needs a license that includes the semantic-cache feature) ---

// SCacheCreateOptions configures the optional knobs of SCACHE.CREATE.
type SCacheCreateOptions struct {
	Metric    string // "cosine" (default), "l2", or "ip"
	Threshold *float64
	TTL       time.Duration
}

// SCacheCreate creates a semantic cache namespace.
func (c *Client) SCacheCreate(ctx context.Context, ns string, dims int, opts *SCacheCreateOptions) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"SCACHE.CREATE", ns, strconv.Itoa(dims)}
	if opts != nil {
		if opts.Metric != "" {
			args = append(args, "METRIC", opts.Metric)
		}
		if opts.Threshold != nil {
			args = append(args, "THRESHOLD", argFloat(*opts.Threshold))
		}
		if opts.TTL > 0 {
			args = append(args, "TTL", strconv.FormatInt(int64(opts.TTL/time.Second), 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SCacheSet stores a query/response pair keyed by its embedding vector.
// usage is optional (nil omits it): when given, it records the original
// (uncached) call's token usage so a later SCacheGet hit attributes the
// tokens and cost that hit did not have to spend (see SCacheStats).
func (c *Client) SCacheSet(ctx context.Context, ns, query, response string, vector []float32, ttl time.Duration, usage *Usage) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := append([]string{"SCACHE.SET", ns, query, response}, vecArgs(vector)...)
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	args = usageArgs(args, usage)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SCacheGet looks up the nearest cached response above the namespace (or
// overridden) similarity threshold.
func (c *Client) SCacheGet(ctx context.Context, ns string, vector []float32, thresholdOverride *float64) *SCacheGetCmd {
	cmd := NewSCacheGetCmd(nil)
	args := append([]string{"SCACHE.GET", ns}, vecArgs(vector)...)
	if thresholdOverride != nil {
		args = append(args, "THRESHOLD", argFloat(*thresholdOverride))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	if !val.IsNil() && len(val.Elems) >= 2 {
		sim, _ := strconv.ParseFloat(val.Elems[1].Str, 64)
		cmd.SetResult(SCacheResult{Hit: true, Response: val.Elems[0].Str, Similarity: sim})
	}
	return cmd
}

// SCacheStats returns entries/hits/misses/hit_rate/p50_us plus how many
// tokens and how much cost recorded usage (see SCacheSet's usage parameter)
// has saved on hits, for ns.
func (c *Client) SCacheStats(ctx context.Context, ns string) *SCacheStatsCmd {
	cmd := NewSCacheStatsCmd(nil)
	val, err := c.process(ctx, "SCACHE.STATS", ns)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	res, perr := parseSCacheStats(val)
	if perr != nil {
		cmd.SetErr(perr)
		return cmd
	}
	cmd.SetResult(res)
	return cmd
}

// SCacheFlush clears all entries in a namespace, keeping the namespace itself.
func (c *Client) SCacheFlush(ctx context.Context, ns string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SCACHE.FLUSH", ns)
	return cmd
}

// SCacheDrop removes a namespace entirely.
func (c *Client) SCacheDrop(ctx context.Context, ns string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SCACHE.DROP", ns)
	return cmd
}

// SCacheList lists semantic cache namespace names.
func (c *Client) SCacheList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SCACHE.LIST")
	return cmd
}

// SCacheThreshold updates a namespace's similarity threshold (0.0-1.0).
func (c *Client) SCacheThreshold(ctx context.Context, ns string, value float64) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SCACHE.THRESHOLD", ns, argFloat(value))
	return cmd
}

// --- PCACHE.* (prompt/LLM response cache, needs a license that includes the prompt-cache feature) ---

// PCacheCreate creates a prompt cache namespace. maxEntries 0 uses the
// engine default; ttl 0 means no default expiry; costPerHit 0 means unset.
func (c *Client) PCacheCreate(ctx context.Context, ns string, maxEntries uint64, ttl time.Duration, costPerHit float64) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"PCACHE.CREATE", ns}
	if maxEntries > 0 {
		args = append(args, "MAX_ENTRIES", strconv.FormatUint(maxEntries, 10))
	}
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	if costPerHit != 0 {
		args = append(args, "COST_PER_HIT", argFloat(costPerHit))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PCacheSet stores a prompt/response pair. meta is an opaque string (JSON
// recommended) and is omitted from the wire when empty. usage is optional
// (nil omits it): when given, it records the original (uncached) call's
// token usage so a later PCacheGet/PCacheGetEx hit attributes the tokens and
// cost that hit did not have to spend (see PCacheStats).
func (c *Client) PCacheSet(ctx context.Context, ns, prompt, response string, ttl time.Duration, meta string, usage *Usage) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"PCACHE.SET", ns, prompt, response}
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	if meta != "" {
		args = append(args, "META", meta)
	}
	args = usageArgs(args, usage)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PCacheGet returns the cached response for a prompt, or Nil on a miss.
func (c *Client) PCacheGet(ctx context.Context, ns, prompt string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.GET", ns, prompt)
	return cmd
}

// PCacheGetEx returns [response, metadata, hit_count] on a hit, or a Nil
// error on a miss.
func (c *Client) PCacheGetEx(ctx context.Context, ns, prompt string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.GETEX", ns, prompt)
	return cmd
}

// PCacheDel removes one cached prompt.
func (c *Client) PCacheDel(ctx context.Context, ns, prompt string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.DEL", ns, prompt)
	return cmd
}

// PCacheStats returns entries/hits/misses/evictions/memory_bytes/hit_rate/
// p50_ns/p95_ns/p99_ns/cost_saved_usd plus how many tokens and how much cost
// recorded usage (see PCacheSet's usage parameter) has saved on hits, for ns.
func (c *Client) PCacheStats(ctx context.Context, ns string) *PCacheStatsCmd {
	cmd := NewPCacheStatsCmd(nil)
	val, err := c.process(ctx, "PCACHE.STATS", ns)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	res, perr := parsePCacheStats(val)
	if perr != nil {
		cmd.SetErr(perr)
		return cmd
	}
	cmd.SetResult(res)
	return cmd
}

// PCacheFlush clears all entries in a namespace, returning how many were removed.
func (c *Client) PCacheFlush(ctx context.Context, ns string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.FLUSH", ns)
	return cmd
}

// PCacheDrop removes a namespace entirely.
func (c *Client) PCacheDrop(ctx context.Context, ns string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.DROP", ns)
	return cmd
}

// PCacheList lists prompt cache namespace names.
func (c *Client) PCacheList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.LIST")
	return cmd
}

// PCacheConfigSet updates one namespace setting. option is one of
// "MAX_ENTRIES", "TTL", "COST_PER_HIT".
func (c *Client) PCacheConfigSet(ctx context.Context, ns, option, value string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PCACHE.CONFIG", ns, "SET", option, value)
	return cmd
}

// --- Keyed caches: TOOL.CACHE.* / NCACHE.* / GUARD.* / RERANK.* ---
// Identical shape per family; generic helpers driven by the wire prefix,
// with thin public methods per family below.

func (c *Client) keyedCacheCreate(ctx context.Context, prefix, ns string, maxEntries uint64, ttl time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{prefix + ".CREATE", ns}
	if maxEntries > 0 {
		args = append(args, "MAX_ENTRIES", strconv.FormatUint(maxEntries, 10))
	}
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

func (c *Client) keyedCacheSet(ctx context.Context, prefix, ns, key, value string, ttl time.Duration, meta string, usage *Usage) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{prefix + ".SET", ns, key, value}
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	if meta != "" {
		args = append(args, "META", meta)
	}
	args = usageArgs(args, usage)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

func (c *Client) keyedCacheGet(ctx context.Context, prefix, ns, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, prefix+".GET", ns, key)
	return cmd
}

func (c *Client) keyedCacheDel(ctx context.Context, prefix, ns, key string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, prefix+".DEL", ns, key)
	return cmd
}

func (c *Client) keyedCacheStats(ctx context.Context, prefix, ns string) *KeyedCacheStatsCmd {
	cmd := NewKeyedCacheStatsCmd(nil)
	val, err := c.process(ctx, prefix+".STATS", ns)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	res, perr := parseKeyedCacheStats(val)
	if perr != nil {
		cmd.SetErr(perr)
		return cmd
	}
	cmd.SetResult(res)
	return cmd
}

func (c *Client) keyedCacheFlush(ctx context.Context, prefix, ns string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, prefix+".FLUSH", ns)
	return cmd
}

func (c *Client) keyedCacheDrop(ctx context.Context, prefix, ns string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, prefix+".DROP", ns)
	return cmd
}

func (c *Client) keyedCacheList(ctx context.Context, prefix string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, prefix+".LIST")
	return cmd
}

// ToolCacheCreate creates a tool-result cache namespace (needs a license that includes the tool-cache feature).
func (c *Client) ToolCacheCreate(ctx context.Context, ns string, maxEntries uint64, ttl time.Duration) *StatusCmd {
	return c.keyedCacheCreate(ctx, "TOOL.CACHE", ns, maxEntries, ttl)
}

// ToolCacheSet stores a tool-call result. usage is optional (nil omits it);
// see PCacheSet for how a recorded usage attributes savings on later hits.
func (c *Client) ToolCacheSet(ctx context.Context, ns, key, value string, ttl time.Duration, meta string, usage *Usage) *StatusCmd {
	return c.keyedCacheSet(ctx, "TOOL.CACHE", ns, key, value, ttl, meta, usage)
}

// ToolCacheGet returns a cached tool-call result, or Nil on a miss.
func (c *Client) ToolCacheGet(ctx context.Context, ns, key string) *StringCmd {
	return c.keyedCacheGet(ctx, "TOOL.CACHE", ns, key)
}

// ToolCacheDel removes a cached entry.
func (c *Client) ToolCacheDel(ctx context.Context, ns, key string) *BoolCmd {
	return c.keyedCacheDel(ctx, "TOOL.CACHE", ns, key)
}

// ToolCacheStats returns entries/hits/misses/evictions/memory_bytes plus
// usage-based savings.
func (c *Client) ToolCacheStats(ctx context.Context, ns string) *KeyedCacheStatsCmd {
	return c.keyedCacheStats(ctx, "TOOL.CACHE", ns)
}

// ToolCacheFlush clears all entries in a namespace, returning the count removed.
func (c *Client) ToolCacheFlush(ctx context.Context, ns string) *IntCmd {
	return c.keyedCacheFlush(ctx, "TOOL.CACHE", ns)
}

// ToolCacheDrop removes a namespace entirely.
func (c *Client) ToolCacheDrop(ctx context.Context, ns string) *BoolCmd {
	return c.keyedCacheDrop(ctx, "TOOL.CACHE", ns)
}

// ToolCacheList lists tool cache namespace names.
func (c *Client) ToolCacheList(ctx context.Context) *StringSliceCmd {
	return c.keyedCacheList(ctx, "TOOL.CACHE")
}

// NCacheCreate creates a negative-result cache namespace (needs a license that includes the negative-cache feature).
func (c *Client) NCacheCreate(ctx context.Context, ns string, maxEntries uint64, ttl time.Duration) *StatusCmd {
	return c.keyedCacheCreate(ctx, "NCACHE", ns, maxEntries, ttl)
}

// NCacheSet stores a negative-cache entry. usage is optional (nil omits
// it); see PCacheSet for how a recorded usage attributes savings on later
// hits.
func (c *Client) NCacheSet(ctx context.Context, ns, key, value string, ttl time.Duration, meta string, usage *Usage) *StatusCmd {
	return c.keyedCacheSet(ctx, "NCACHE", ns, key, value, ttl, meta, usage)
}

// NCacheGet returns a negative-cache entry, or Nil on a miss.
func (c *Client) NCacheGet(ctx context.Context, ns, key string) *StringCmd {
	return c.keyedCacheGet(ctx, "NCACHE", ns, key)
}

// NCacheDel removes a cached entry.
func (c *Client) NCacheDel(ctx context.Context, ns, key string) *BoolCmd {
	return c.keyedCacheDel(ctx, "NCACHE", ns, key)
}

// NCacheStats returns entries/hits/misses/evictions/memory_bytes plus
// usage-based savings.
func (c *Client) NCacheStats(ctx context.Context, ns string) *KeyedCacheStatsCmd {
	return c.keyedCacheStats(ctx, "NCACHE", ns)
}

// NCacheFlush clears all entries in a namespace, returning the count removed.
func (c *Client) NCacheFlush(ctx context.Context, ns string) *IntCmd {
	return c.keyedCacheFlush(ctx, "NCACHE", ns)
}

// NCacheDrop removes a namespace entirely.
func (c *Client) NCacheDrop(ctx context.Context, ns string) *BoolCmd {
	return c.keyedCacheDrop(ctx, "NCACHE", ns)
}

// NCacheList lists negative cache namespace names.
func (c *Client) NCacheList(ctx context.Context) *StringSliceCmd {
	return c.keyedCacheList(ctx, "NCACHE")
}

// GuardCacheCreate creates a guardrail decision cache namespace (needs a license that includes the guard-cache feature).
func (c *Client) GuardCacheCreate(ctx context.Context, ns string, maxEntries uint64, ttl time.Duration) *StatusCmd {
	return c.keyedCacheCreate(ctx, "GUARD", ns, maxEntries, ttl)
}

// GuardCacheSet stores a guardrail decision. usage is optional (nil omits
// it); see PCacheSet for how a recorded usage attributes savings on later
// hits.
func (c *Client) GuardCacheSet(ctx context.Context, ns, key, value string, ttl time.Duration, meta string, usage *Usage) *StatusCmd {
	return c.keyedCacheSet(ctx, "GUARD", ns, key, value, ttl, meta, usage)
}

// GuardCacheGet returns a cached guardrail decision, or Nil on a miss.
func (c *Client) GuardCacheGet(ctx context.Context, ns, key string) *StringCmd {
	return c.keyedCacheGet(ctx, "GUARD", ns, key)
}

// GuardCacheDel removes a cached entry.
func (c *Client) GuardCacheDel(ctx context.Context, ns, key string) *BoolCmd {
	return c.keyedCacheDel(ctx, "GUARD", ns, key)
}

// GuardCacheStats returns entries/hits/misses/evictions/memory_bytes plus
// usage-based savings.
func (c *Client) GuardCacheStats(ctx context.Context, ns string) *KeyedCacheStatsCmd {
	return c.keyedCacheStats(ctx, "GUARD", ns)
}

// GuardCacheFlush clears all entries in a namespace, returning the count removed.
func (c *Client) GuardCacheFlush(ctx context.Context, ns string) *IntCmd {
	return c.keyedCacheFlush(ctx, "GUARD", ns)
}

// GuardCacheDrop removes a namespace entirely.
func (c *Client) GuardCacheDrop(ctx context.Context, ns string) *BoolCmd {
	return c.keyedCacheDrop(ctx, "GUARD", ns)
}

// GuardCacheList lists guard cache namespace names.
func (c *Client) GuardCacheList(ctx context.Context) *StringSliceCmd {
	return c.keyedCacheList(ctx, "GUARD")
}

// RerankCacheCreate creates a reranker cache namespace (needs a license that includes the rerank-cache feature).
func (c *Client) RerankCacheCreate(ctx context.Context, ns string, maxEntries uint64, ttl time.Duration) *StatusCmd {
	return c.keyedCacheCreate(ctx, "RERANK", ns, maxEntries, ttl)
}

// RerankCacheSet stores a reranker result. usage is optional (nil omits
// it); see PCacheSet for how a recorded usage attributes savings on later
// hits.
func (c *Client) RerankCacheSet(ctx context.Context, ns, key, value string, ttl time.Duration, meta string, usage *Usage) *StatusCmd {
	return c.keyedCacheSet(ctx, "RERANK", ns, key, value, ttl, meta, usage)
}

// RerankCacheGet returns a cached reranker result, or Nil on a miss.
func (c *Client) RerankCacheGet(ctx context.Context, ns, key string) *StringCmd {
	return c.keyedCacheGet(ctx, "RERANK", ns, key)
}

// RerankCacheDel removes a cached entry.
func (c *Client) RerankCacheDel(ctx context.Context, ns, key string) *BoolCmd {
	return c.keyedCacheDel(ctx, "RERANK", ns, key)
}

// RerankCacheStats returns entries/hits/misses/evictions/memory_bytes plus
// usage-based savings.
func (c *Client) RerankCacheStats(ctx context.Context, ns string) *KeyedCacheStatsCmd {
	return c.keyedCacheStats(ctx, "RERANK", ns)
}

// RerankCacheFlush clears all entries in a namespace, returning the count removed.
func (c *Client) RerankCacheFlush(ctx context.Context, ns string) *IntCmd {
	return c.keyedCacheFlush(ctx, "RERANK", ns)
}

// RerankCacheDrop removes a namespace entirely.
func (c *Client) RerankCacheDrop(ctx context.Context, ns string) *BoolCmd {
	return c.keyedCacheDrop(ctx, "RERANK", ns)
}

// RerankCacheList lists rerank cache namespace names.
func (c *Client) RerankCacheList(ctx context.Context) *StringSliceCmd {
	return c.keyedCacheList(ctx, "RERANK")
}

// --- LLMSTATS.* (needs a license that includes the LLM-stats feature) ---

// LLMStatsBucket creates a stats bucket (idempotent).
func (c *Client) LLMStatsBucket(ctx context.Context, name string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMSTATS.BUCKET", name)
	return cmd
}

// LLMStatsRecordOptions configures the optional trailing keywords of
// LLMSTATS.RECORD. Error nil omits the ERROR option. When Model is
// non-empty, the server computes cost from its price table (see
// LLMStatsPriceSet) and ignores the positional costUSD argument (pass 0);
// CacheWrite5m/CacheWrite1h (Anthropic cache-write tokens by TTL tier) are
// only meaningful together with Model.
type LLMStatsRecordOptions struct {
	Error        *bool
	Model        string
	CacheWrite5m uint64
	CacheWrite1h uint64
}

// LLMStatsRecord records one LLM call's usage. opts may be nil.
func (c *Client) LLMStatsRecord(ctx context.Context, bucket string, inputTokens, outputTokens, cachedTokens uint64, costUSD float64, latency time.Duration, opts *LLMStatsRecordOptions) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{
		"LLMSTATS.RECORD", bucket,
		strconv.FormatUint(inputTokens, 10),
		strconv.FormatUint(outputTokens, 10),
		strconv.FormatUint(cachedTokens, 10),
		argFloat(costUSD),
		strconv.FormatInt(latency.Milliseconds(), 10),
	}
	if opts != nil {
		if opts.Error != nil {
			v := "0"
			if *opts.Error {
				v = "1"
			}
			args = append(args, "ERROR", v)
		}
		if opts.Model != "" {
			args = append(args, "MODEL", opts.Model)
		}
		if opts.CacheWrite5m > 0 {
			args = append(args, "CACHEWRITE5M", strconv.FormatUint(opts.CacheWrite5m, 10))
		}
		if opts.CacheWrite1h > 0 {
			args = append(args, "CACHEWRITE1H", strconv.FormatUint(opts.CacheWrite1h, 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// LLMStatsGet returns requests/errors/input_tokens/output_tokens/
// cached_tokens/cost_usd/avg_latency_ms/requests_1m/tokens_1m/cost_usd_1m/
// cache_write_5m_tokens/cache_write_1h_tokens/unpriced_requests for bucket.
func (c *Client) LLMStatsGet(ctx context.Context, bucket string) *LLMStatsCmd {
	cmd := NewLLMStatsCmd(nil)
	val, err := c.process(ctx, "LLMSTATS.GET", bucket)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	res, perr := parseLLMStats(val)
	if perr != nil {
		cmd.SetErr(perr)
		return cmd
	}
	cmd.SetResult(res)
	return cmd
}

// LLMStatsReset zeroes a bucket's counters.
func (c *Client) LLMStatsReset(ctx context.Context, bucket string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMSTATS.RESET", bucket)
	return cmd
}

// LLMStatsDrop removes a bucket.
func (c *Client) LLMStatsDrop(ctx context.Context, bucket string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMSTATS.DROP", bucket)
	return cmd
}

// LLMStatsList lists bucket names.
func (c *Client) LLMStatsList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMSTATS.LIST")
	return cmd
}

// --- LLMSTATS.PRICE (custom price table shared with LLMSTATS.RECORD MODEL
// and LLMBUDGET.CONSUME MODEL; needs a license that includes the LLM-stats feature) ---

// LLMPriceSetOptions configures the optional cache price overrides of
// LLMStatsPriceSet. Nil fields default (server side) to 0.1x (cache read),
// 1.25x (5-minute cache write) and 2x (1-hour cache write) the input price.
type LLMPriceSetOptions struct {
	CacheReadUSDPerMtok    *float64
	CacheWrite5mUSDPerMtok *float64
	CacheWrite1hUSDPerMtok *float64
}

// LLMStatsPriceSet sets (or replaces) a custom price override for model.
// Prices are USD per 1,000,000 tokens.
func (c *Client) LLMStatsPriceSet(ctx context.Context, model string, inputUSDPerMtok, outputUSDPerMtok float64, opts *LLMPriceSetOptions) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{
		"LLMSTATS.PRICE", "SET", model,
		"INPUT", argUSD(inputUSDPerMtok),
		"OUTPUT", argUSD(outputUSDPerMtok),
	}
	if opts != nil {
		if opts.CacheReadUSDPerMtok != nil {
			args = append(args, "CACHEREAD", argUSD(*opts.CacheReadUSDPerMtok))
		}
		if opts.CacheWrite5mUSDPerMtok != nil {
			args = append(args, "CACHEWRITE5M", argUSD(*opts.CacheWrite5mUSDPerMtok))
		}
		if opts.CacheWrite1hUSDPerMtok != nil {
			args = append(args, "CACHEWRITE1H", argUSD(*opts.CacheWrite1hUSDPerMtok))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// LLMStatsPriceGet returns model's price sheet. Unknown models reply with
// Source "unpriced" and Priced false rather than an error.
func (c *Client) LLMStatsPriceGet(ctx context.Context, model string) *LLMPriceCmd {
	cmd := NewLLMPriceCmd(nil)
	val, err := c.process(ctx, "LLMSTATS.PRICE", "GET", model)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	res, perr := parseLLMPrice(val)
	if perr != nil {
		cmd.SetErr(perr)
		return cmd
	}
	cmd.SetResult(res)
	return cmd
}

// LLMStatsPriceList returns every known model's price sheet (built-in and
// custom).
func (c *Client) LLMStatsPriceList(ctx context.Context) *LLMPriceSliceCmd {
	cmd := NewLLMPriceSliceCmd(nil)
	val, err := c.process(ctx, "LLMSTATS.PRICE", "LIST")
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	if val.IsNil() {
		return cmd
	}
	out := make([]LLMPriceResult, 0, len(val.Elems))
	for _, e := range val.Elems {
		res, perr := parseLLMPrice(e)
		if perr != nil {
			cmd.SetErr(perr)
			return cmd
		}
		out = append(out, res)
	}
	cmd.SetResult(out)
	return cmd
}

// LLMStatsPriceDel removes a custom price override, returning true iff one
// existed (the model then falls back to its built-in price, if any).
func (c *Client) LLMStatsPriceDel(ctx context.Context, model string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMSTATS.PRICE", "DEL", model)
	return cmd
}

// --- LLMBUDGET.* (needs a license that includes the LLM-budget feature) ---

// LLMBudgetSet defines (or redefines) a budget.
func (c *Client) LLMBudgetSet(ctx context.Context, name string, tokenLimit uint64, costLimitUSD float64, window time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMBUDGET.SET", name,
		strconv.FormatUint(tokenLimit, 10),
		argUSD(costLimitUSD),
		strconv.FormatInt(int64(window/time.Second), 10))
	return cmd
}

// LLMBudgetConsume atomically consumes tokens/cost, returning whether the
// call is allowed under the current window. usage is optional (nil omits
// it); when given, the server ignores tokens/costUSD (pass 0 and 0 for
// them) and instead sums usage's token counts and prices the call from its
// model, exactly as LLMStatsRecord's Model option does.
func (c *Client) LLMBudgetConsume(ctx context.Context, name string, tokens uint64, costUSD float64, usage *Usage) *BoolCmd {
	cmd := NewBoolCmd(nil)
	args := []string{"LLMBUDGET.CONSUME", name,
		strconv.FormatUint(tokens, 10), argFloat(costUSD)}
	args = usageArgs(args, usage)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// LLMBudgetStatus returns tokens_used/token_limit/cost_used_usd/
// cost_limit_usd/window_seconds/window_remaining/requests_allowed/
// requests_rejected/unpriced_requests for name.
func (c *Client) LLMBudgetStatus(ctx context.Context, name string) *LLMBudgetStatusCmd {
	cmd := NewLLMBudgetStatusCmd(nil)
	val, err := c.process(ctx, "LLMBUDGET.STATUS", name)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	res, perr := parseLLMBudgetStatus(val)
	if perr != nil {
		cmd.SetErr(perr)
		return cmd
	}
	cmd.SetResult(res)
	return cmd
}

// LLMBudgetReset zeroes a budget's usage counters.
func (c *Client) LLMBudgetReset(ctx context.Context, name string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMBUDGET.RESET", name)
	return cmd
}

// LLMBudgetDrop removes a budget.
func (c *Client) LLMBudgetDrop(ctx context.Context, name string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMBUDGET.DROP", name)
	return cmd
}

// LLMBudgetList lists budget names.
func (c *Client) LLMBudgetList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLMBUDGET.LIST")
	return cmd
}

// --- PROMPT.* (versioned prompt registry, needs a license that includes the prompt-registry feature) ---

// PromptPut stores a new version of a prompt template, returning the new
// version number. meta is omitted from the wire when empty.
func (c *Client) PromptPut(ctx context.Context, name, content, meta string) *IntCmd {
	cmd := NewIntCmd(nil)
	if meta != "" {
		c.processCmd(ctx, &cmd.Cmd, "PROMPT.PUT", name, content, "META", meta)
	} else {
		c.processCmd(ctx, &cmd.Cmd, "PROMPT.PUT", name, content)
	}
	return cmd
}

// PromptGet returns [version, content, metadata, created_unix] for a
// specific version, or the latest when version is 0.
func (c *Client) PromptGet(ctx context.Context, name string, version uint32) *SliceCmd {
	cmd := NewSliceCmd(nil)
	if version > 0 {
		c.processCmd(ctx, &cmd.Cmd, "PROMPT.GET", name, strconv.FormatUint(uint64(version), 10))
	} else {
		c.processCmd(ctx, &cmd.Cmd, "PROMPT.GET", name)
	}
	return cmd
}

// PromptLatest returns [version, content, metadata, created_unix] for the
// active version of name.
func (c *Client) PromptLatest(ctx context.Context, name string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PROMPT.LATEST", name)
	return cmd
}

// PromptSetActive marks a stored version as the active/latest one.
func (c *Client) PromptSetActive(ctx context.Context, name string, version uint32) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PROMPT.SETACTIVE", name, strconv.FormatUint(uint64(version), 10))
	return cmd
}

// PromptVersions returns all stored version numbers for name.
func (c *Client) PromptVersions(ctx context.Context, name string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PROMPT.VERSIONS", name)
	return cmd
}

// PromptDrop removes a template and all its versions.
func (c *Client) PromptDrop(ctx context.Context, name string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PROMPT.DROP", name)
	return cmd
}

// PromptList lists template names.
func (c *Client) PromptList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PROMPT.LIST")
	return cmd
}

// --- EMBED.DEDUP.* (needs a license that includes the embed-dedup feature) ---

// EmbedDedupPut stores an embedding under a content hash, or increments the
// refcount of an existing one. Returns true when it was newly stored.
func (c *Client) EmbedDedupPut(ctx context.Context, ns, hash, model, embedding string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.PUT", ns, hash, model, embedding)
	return cmd
}

// EmbedDedupGet returns the embedding stored for hash, or Nil on a miss.
func (c *Client) EmbedDedupGet(ctx context.Context, ns, hash string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.GET", ns, hash)
	return cmd
}

// EmbedDedupRelease decrements the refcount for hash, freeing it at zero.
func (c *Client) EmbedDedupRelease(ctx context.Context, ns, hash string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.RELEASE", ns, hash)
	return cmd
}

// EmbedDedupStats returns hits, misses, entries for ns.
func (c *Client) EmbedDedupStats(ctx context.Context, ns string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.STATS", ns)
	return cmd
}

// EmbedDedupDrop removes an entire namespace.
func (c *Client) EmbedDedupDrop(ctx context.Context, ns string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.DROP", ns)
	return cmd
}

// EmbedDedupPurge purges expired/unreferenced entries in a namespace.
func (c *Client) EmbedDedupPurge(ctx context.Context, ns string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.PURGE", ns)
	return cmd
}

// EmbedDedupList lists namespace names.
func (c *Client) EmbedDedupList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EMBED.DEDUP.LIST")
	return cmd
}

// --- AMEM.* (AI agent memory, needs a license that includes the agent-memory feature) ---

// AmemConvCreate starts a conversation memory session. window 0 uses the
// engine default; ttl 0 means no expiry.
func (c *Client) AmemConvCreate(ctx context.Context, session string, window uint32, ttl time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"AMEM.CONV.CREATE", session}
	if window > 0 {
		args = append(args, "WINDOW", strconv.FormatUint(uint64(window), 10))
	}
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// AmemConvAppend appends one turn to a conversation session.
func (c *Client) AmemConvAppend(ctx context.Context, session, role, content string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.CONV.APPEND", session, role, content)
	return cmd
}

// AmemConvGet returns every turn in a session. Each element is a flat
// ["role", value, "content", value] array, per the server's reply shape.
func (c *Client) AmemConvGet(ctx context.Context, session string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.CONV.GET", session)
	return cmd
}

// AmemConvWindow returns the last n turns, same shape as AmemConvGet.
func (c *Client) AmemConvWindow(ctx context.Context, session string, n uint32) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.CONV.WINDOW", session, strconv.FormatUint(uint64(n), 10))
	return cmd
}

// AmemConvClear empties a session's turns without dropping the session.
func (c *Client) AmemConvClear(ctx context.Context, session string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.CONV.CLEAR", session)
	return cmd
}

// AmemConvDrop removes a conversation session entirely.
func (c *Client) AmemConvDrop(ctx context.Context, session string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.CONV.DROP", session)
	return cmd
}

// AmemSemCreate creates a per-agent semantic memory vector store.
func (c *Client) AmemSemCreate(ctx context.Context, agent string, dims uint32) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SEM.CREATE", agent, strconv.FormatUint(uint64(dims), 10))
	return cmd
}

// AmemSemStore stores a memory with its embedding. ttl 0 means no expiry.
func (c *Client) AmemSemStore(ctx context.Context, agent, key, content string, vec []float32, ttl time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := append([]string{"AMEM.SEM.STORE", agent, key, content}, vecArgs(vec)...)
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// AmemSemRecall returns the k most similar memories to vec. Each element is
// a flat ["key", value, "content", value, "similarity", value] array.
func (c *Client) AmemSemRecall(ctx context.Context, agent string, k uint32, vec []float32) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := append([]string{"AMEM.SEM.RECALL", agent, strconv.FormatUint(uint64(k), 10)}, vecArgs(vec)...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// AmemSemForget deletes one semantic memory.
func (c *Client) AmemSemForget(ctx context.Context, agent, key string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SEM.FORGET", agent, key)
	return cmd
}

// AmemSemList lists the keys stored for an agent.
func (c *Client) AmemSemList(ctx context.Context, agent string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SEM.LIST", agent)
	return cmd
}

// AmemWorkSet writes a scratch key/value for a session. ttl 0 means no expiry.
func (c *Client) AmemWorkSet(ctx context.Context, session, key, value string, ttl time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"AMEM.WORK.SET", session, key, value}
	if ttl > 0 {
		args = append(args, "TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// AmemWorkGet reads a scratch value. Note: the server cannot distinguish a
// missing key from one holding an empty string; both return Nil.
func (c *Client) AmemWorkGet(ctx context.Context, session, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.WORK.GET", session, key)
	return cmd
}

// AmemWorkDel removes a scratch key.
func (c *Client) AmemWorkDel(ctx context.Context, session, key string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.WORK.DEL", session, key)
	return cmd
}

// AmemWorkClear empties a session's scratch keys.
func (c *Client) AmemWorkClear(ctx context.Context, session string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.WORK.CLEAR", session)
	return cmd
}

// AmemWorkList lists a session's scratch keys.
func (c *Client) AmemWorkList(ctx context.Context, session string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.WORK.LIST", session)
	return cmd
}

// AmemEpisQueryOptions configures the optional filters of AMEM.EPIS.QUERY.
type AmemEpisQueryOptions struct {
	Type  string
	From  *int64
	To    *int64
	Limit *uint32
}

// AmemEpisLog appends one episodic event. metadata is omitted from the wire
// when empty.
func (c *Client) AmemEpisLog(ctx context.Context, agent, eventType, content, metadata string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	if metadata != "" {
		c.processCmd(ctx, &cmd.Cmd, "AMEM.EPIS.LOG", agent, eventType, content, "METADATA", metadata)
	} else {
		c.processCmd(ctx, &cmd.Cmd, "AMEM.EPIS.LOG", agent, eventType, content)
	}
	return cmd
}

// AmemEpisQuery returns matching events. Each element is a flat ["type",
// value, "content", value, "metadata", value, "timestamp", value] array.
func (c *Client) AmemEpisQuery(ctx context.Context, agent string, opts *AmemEpisQueryOptions) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := []string{"AMEM.EPIS.QUERY", agent}
	if opts != nil {
		if opts.Type != "" {
			args = append(args, "TYPE", opts.Type)
		}
		if opts.From != nil {
			args = append(args, "FROM", strconv.FormatInt(*opts.From, 10))
		}
		if opts.To != nil {
			args = append(args, "TO", strconv.FormatInt(*opts.To, 10))
		}
		if opts.Limit != nil {
			args = append(args, "LIMIT", strconv.FormatUint(uint64(*opts.Limit), 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// AmemEpisClear removes every event for an agent.
func (c *Client) AmemEpisClear(ctx context.Context, agent string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.EPIS.CLEAR", agent)
	return cmd
}

// AmemEpisStats returns total_events, oldest_ts, newest_ts for an agent.
func (c *Client) AmemEpisStats(ctx context.Context, agent string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.EPIS.STATS", agent)
	return cmd
}

// AmemSearch searches semantic, episodic and working memory together. Each
// element is a flat ["source", value, "key", value, "content", value,
// "similarity", value] array.
func (c *Client) AmemSearch(ctx context.Context, agent string, k uint32, vec []float32) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := append([]string{"AMEM.SEARCH", agent, strconv.FormatUint(uint64(k), 10)}, vecArgs(vec)...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// --- AMEM.SUMMARY.* (needs a license that includes the agent-memory-summary feature) ---

// AmemSummarySet replaces an agent's rolling summary, returning the new version.
func (c *Client) AmemSummarySet(ctx context.Context, agent, summary string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SUMMARY.SET", agent, summary)
	return cmd
}

// AmemSummaryAppend appends to an agent's rolling summary, returning the new version.
func (c *Client) AmemSummaryAppend(ctx context.Context, agent, extra string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SUMMARY.APPEND", agent, extra)
	return cmd
}

// AmemSummaryGet returns [summary, version, updated_unix], or Nil if unset.
func (c *Client) AmemSummaryGet(ctx context.Context, agent string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SUMMARY.GET", agent)
	return cmd
}

// AmemSummaryDrop removes an agent's summary.
func (c *Client) AmemSummaryDrop(ctx context.Context, agent string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SUMMARY.DROP", agent)
	return cmd
}

// AmemSummaryList lists agent ids that have a stored summary.
func (c *Client) AmemSummaryList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "AMEM.SUMMARY.LIST")
	return cmd
}

// --- RAG.* (needs a license that includes the RAG-pipeline feature) ---

// RAGCreateOptions configures the optional knobs of RAG.CREATE.
type RAGCreateOptions struct {
	Strategy     string // "fixed" (default), "sentence", or "paragraph"
	ChunkSize    uint32
	ChunkOverlap uint32
}

// RAGCreate creates a retrieval-augmented-generation pipeline.
func (c *Client) RAGCreate(ctx context.Context, name string, dims uint32, opts *RAGCreateOptions) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"RAG.CREATE", name, strconv.FormatUint(uint64(dims), 10)}
	if opts != nil {
		if opts.Strategy != "" {
			args = append(args, "STRATEGY", opts.Strategy)
		}
		if opts.ChunkSize > 0 {
			args = append(args, "CHUNK_SIZE", strconv.FormatUint(uint64(opts.ChunkSize), 10))
		}
		if opts.ChunkOverlap > 0 {
			args = append(args, "CHUNK_OVERLAP", strconv.FormatUint(uint64(opts.ChunkOverlap), 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// RAGIngest chunks and indexes a document. meta and vectors may both be
// nil/empty; when vectors is given it must have one entry per chunk the
// pipeline's strategy will produce. Returns the number of chunks ingested.
func (c *Client) RAGIngest(ctx context.Context, pipeline, docID, text string, meta map[string]string, vectors [][]float32) *IntCmd {
	cmd := NewIntCmd(nil)
	args := []string{"RAG.INGEST", pipeline, docID, text}
	if len(meta) > 0 {
		args = append(args, "META")
		for k, v := range meta {
			args = append(args, k, v)
		}
	}
	if len(vectors) > 0 {
		args = append(args, "VECTORS")
		for _, v := range vectors {
			args = append(args, vecToCSV(v))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// RAGSearchOptions configures the optional knobs of RAG.SEARCH.
type RAGSearchOptions struct {
	K      uint32 // default 5
	Alpha  *float64
	Vector []float32
	Filter map[string]string
}

// RAGSearch runs a hybrid (BM25 + vector) search. Each result element is a
// flat array: chunk_id, doc_id, text, score, vector_score, bm25_score,
// followed by metadata key/value pairs.
func (c *Client) RAGSearch(ctx context.Context, pipeline, queryText string, opts *RAGSearchOptions) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := []string{"RAG.SEARCH", pipeline, queryText}
	if opts != nil {
		if opts.K > 0 {
			args = append(args, "K", strconv.FormatUint(uint64(opts.K), 10))
		}
		if opts.Alpha != nil {
			args = append(args, "ALPHA", argFloat(*opts.Alpha))
		}
		if len(opts.Vector) > 0 {
			args = append(args, "VECTOR", vecToCSV(opts.Vector))
		}
		if len(opts.Filter) > 0 {
			args = append(args, "FILTER")
			for k, v := range opts.Filter {
				args = append(args, k, v)
			}
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// RAGStats returns name, dimensions, totalDocs, totalChunks, totalIngested,
// totalQueries, chunkSize, chunkStrategy, chunkOverlap.
func (c *Client) RAGStats(ctx context.Context, pipeline string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RAG.STATS", pipeline)
	return cmd
}

// RAGDrop removes a pipeline and its indexed vectors.
func (c *Client) RAGDrop(ctx context.Context, pipeline string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RAG.DROP", pipeline)
	return cmd
}

// RAGList lists pipeline names.
func (c *Client) RAGList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RAG.LIST")
	return cmd
}

// --- RATELIMIT.* (needs a license that includes the rate-limiting feature) ---

// RateLimitCreateOptions configures the optional knobs of RATELIMIT.CREATE.
type RateLimitCreateOptions struct {
	Algo   string // "sliding_window" (default), "token_bucket", or "leaky_bucket"
	Refill float64
	Drain  float64
}

// RateLimitCreate defines a named rate limiter.
func (c *Client) RateLimitCreate(ctx context.Context, name string, limit uint64, window time.Duration, opts *RateLimitCreateOptions) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"RATELIMIT.CREATE", name, strconv.FormatUint(limit, 10), strconv.FormatInt(window.Milliseconds(), 10)}
	if opts != nil {
		if opts.Algo != "" {
			args = append(args, "ALGO", opts.Algo)
		}
		if opts.Refill != 0 {
			args = append(args, "REFILL", argFloat(opts.Refill))
		}
		if opts.Drain != 0 {
			args = append(args, "DRAIN", argFloat(opts.Drain))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// RateLimitCheck consumes cost tokens (default 1) against key's budget in
// limiter name, advancing limiter state.
func (c *Client) RateLimitCheck(ctx context.Context, name, key string, cost uint64) *RateLimitCmd {
	cmd := NewRateLimitCmd(nil)
	args := []string{"RATELIMIT.CHECK", name, key}
	if cost > 0 {
		args = append(args, "COST", strconv.FormatUint(cost, 10))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	if len(val.Elems) >= 5 {
		cmd.SetResult(RateLimitResult{
			Allowed:      valInt(val.Elems[0]) != 0,
			Remaining:    valInt(val.Elems[1]),
			RetryAfterMs: valInt(val.Elems[2]),
			Limit:        valInt(val.Elems[3]),
			Current:      valInt(val.Elems[4]),
		})
	}
	return cmd
}

// RateLimitPeek reports the state for key without consuming from it.
func (c *Client) RateLimitPeek(ctx context.Context, name, key string) *RateLimitCmd {
	cmd := NewRateLimitCmd(nil)
	val, err := c.process(ctx, "RATELIMIT.PEEK", name, key)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	if len(val.Elems) >= 5 {
		cmd.SetResult(RateLimitResult{
			Allowed:      valInt(val.Elems[0]) != 0,
			Remaining:    valInt(val.Elems[1]),
			RetryAfterMs: valInt(val.Elems[2]),
			Limit:        valInt(val.Elems[3]),
			Current:      valInt(val.Elems[4]),
		})
	}
	return cmd
}

// RateLimitReset clears state for one key within a limiter.
func (c *Client) RateLimitReset(ctx context.Context, name, key string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RATELIMIT.RESET", name, key)
	return cmd
}

// RateLimitStats returns name, algo, limit, window_ms, unique_keys,
// total_allowed, total_denied.
func (c *Client) RateLimitStats(ctx context.Context, name string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RATELIMIT.STATS", name)
	return cmd
}

// RateLimitDrop removes a limiter definition.
func (c *Client) RateLimitDrop(ctx context.Context, name string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RATELIMIT.DROP", name)
	return cmd
}

// RateLimitList lists limiter names.
func (c *Client) RateLimitList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RATELIMIT.LIST")
	return cmd
}

// --- TS.* (time series, needs a license that includes the time-series feature) ---

// TSCreate creates a time series. retention 0 means unlimited; labels may be nil.
func (c *Client) TSCreate(ctx context.Context, key string, retention time.Duration, labels map[string]string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"TS.CREATE", key}
	if retention > 0 {
		args = append(args, "RETENTION", strconv.FormatInt(retention.Milliseconds(), 10))
	}
	if len(labels) > 0 {
		args = append(args, "LABELS")
		for k, v := range labels {
			args = append(args, k, v)
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// TSAdd appends a sample. timestampMs nil uses the server's current time
// ("*" on the wire). Returns the timestamp (ms) actually used.
func (c *Client) TSAdd(ctx context.Context, key string, timestampMs *int64, value float64) *IntCmd {
	cmd := NewIntCmd(nil)
	ts := "*"
	if timestampMs != nil {
		ts = strconv.FormatInt(*timestampMs, 10)
	}
	c.processCmd(ctx, &cmd.Cmd, "TS.ADD", key, ts, argFloat(value))
	return cmd
}

// TSRange returns [timestamp_ms, value] pairs between fromMs and toMs
// (inclusive). count 0 means no limit.
func (c *Client) TSRange(ctx context.Context, key string, fromMs, toMs int64, count uint64) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := []string{"TS.RANGE", key, strconv.FormatInt(fromMs, 10), strconv.FormatInt(toMs, 10)}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatUint(count, 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// TSAgg returns bucketed [timestamp_ms, value] pairs. aggType is one of
// "avg", "min", "max", "sum", "count".
func (c *Client) TSAgg(ctx context.Context, key string, fromMs, toMs int64, bucket time.Duration, aggType string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TS.AGG", key,
		strconv.FormatInt(fromMs, 10), strconv.FormatInt(toMs, 10),
		strconv.FormatInt(bucket.Milliseconds(), 10), aggType)
	return cmd
}

// TSInfo returns key, totalSamples, firstTimestamp, lastTimestamp,
// lastValue, retentionMs, memoryBytes, labels, rules.
func (c *Client) TSInfo(ctx context.Context, key string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TS.INFO", key)
	return cmd
}

// TSDelete removes a time series.
func (c *Client) TSDelete(ctx context.Context, key string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TS.DELETE", key)
	return cmd
}

// TSRetention updates a series' retention window.
func (c *Client) TSRetention(ctx context.Context, key string, retention time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TS.RETENTION", key, strconv.FormatInt(retention.Milliseconds(), 10))
	return cmd
}

// TSDownsample attaches a live downsample rule from src into dest.
func (c *Client) TSDownsample(ctx context.Context, src, dest, aggType string, bucket time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TS.DOWNSAMPLE", src, dest, aggType, strconv.FormatInt(bucket.Milliseconds(), 10))
	return cmd
}

// TSList lists series keys, optionally filtered to one label/value pair
// (pass "" for both to list every series).
func (c *Client) TSList(ctx context.Context, labelKey, labelValue string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	if labelKey != "" {
		c.processCmd(ctx, &cmd.Cmd, "TS.LIST", "LABEL", labelKey, labelValue)
	} else {
		c.processCmd(ctx, &cmd.Cmd, "TS.LIST")
	}
	return cmd
}

// --- FEATURE.* (feature store, needs a license that includes the feature-store feature) ---

// FeatureCreate defines a feature group. schema maps feature name to type,
// each one of "int64", "double", "string", "int_array", "double_array".
func (c *Client) FeatureCreate(ctx context.Context, group, description string, schema map[string]string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"FEATURE.CREATE", group, description, "SCHEMA"}
	for k, v := range schema {
		args = append(args, k, v)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// FeatureSet writes feature values for one entity.
func (c *Client) FeatureSet(ctx context.Context, group, entity string, features map[string]string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"FEATURE.SET", group, entity}
	for k, v := range features {
		args = append(args, k, v)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// FeatureGet reads feature values for one entity. names is empty to fetch
// every feature. Reply is nil (Nil error) if the entity has nothing stored,
// else a 3-element array: [flat name/value pairs, updated_at_ms, freshness_ms].
func (c *Client) FeatureGet(ctx context.Context, group, entity string, names ...string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := append([]string{"FEATURE.GET", group, entity}, names...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// FeatureBatch reads feature values for several entities in one call. Reply
// has one entry per entity, each either nil or the FeatureGet shape.
func (c *Client) FeatureBatch(ctx context.Context, group string, entities []string, names ...string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := append([]string{"FEATURE.BATCH", group}, entities...)
	if len(names) > 0 {
		args = append(args, "FEATURES")
		args = append(args, names...)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// FeatureSchema returns [name, type] pairs for a group.
func (c *Client) FeatureSchema(ctx context.Context, group string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "FEATURE.SCHEMA", group)
	return cmd
}

// FeatureList lists feature group names.
func (c *Client) FeatureList(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "FEATURE.LIST")
	return cmd
}

// FeatureDrop removes a feature group.
func (c *Client) FeatureDrop(ctx context.Context, group string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "FEATURE.DROP", group)
	return cmd
}

// FeatureStats returns entities, totalReads, totalWrites, memoryBytes.
func (c *Client) FeatureStats(ctx context.Context, group string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "FEATURE.STATS", group)
	return cmd
}

// --- OBSERVE / ALERTS (needs a license that includes the observability feature, admin) ---

// ObserveInfo returns uptime_seconds, connected_clients, total_commands,
// used_memory_bytes, peak_memory_bytes, total_connections, enterprise_features.
func (c *Client) ObserveInfo(ctx context.Context) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "OBSERVE", "INFO")
	return cmd
}

// ObserveMetrics returns uptime_seconds, connected_clients, total_commands,
// total_connections, used_memory_bytes, peak_memory_bytes, slowlog_length,
// alerts_sent, alerts_failed, num_workers.
func (c *Client) ObserveMetrics(ctx context.Context) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "OBSERVE", "METRICS")
	return cmd
}

// AlertsInfo returns the current alerting configuration and counters.
func (c *Client) AlertsInfo(ctx context.Context) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ALERTS", "INFO")
	return cmd
}

// AlertsTest fires a connectivity-test alert. severity is one of "info",
// "warning", "critical" ("" uses the dispatcher's configured minimum);
// message "" uses the built-in wording.
func (c *Client) AlertsTest(ctx context.Context, severity, message string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"ALERTS", "TEST"}
	if severity != "" {
		args = append(args, severity)
		if message != "" {
			args = append(args, message)
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// AlertsSetURL updates the alert webhook URL at runtime. Pass "" to disable alerting.
func (c *Client) AlertsSetURL(ctx context.Context, url string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ALERTS", "SETURL", url)
	return cmd
}

// AlertsSetSeverity sets the minimum severity that is dispatched. One of
// "info", "warning", "critical".
func (c *Client) AlertsSetSeverity(ctx context.Context, severity string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ALERTS", "SETSEVERITY", severity)
	return cmd
}

// AlertsEnable turns on alert dispatch without touching the webhook URL.
func (c *Client) AlertsEnable(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ALERTS", "ON")
	return cmd
}

// AlertsDisable turns off alert dispatch without clearing the webhook URL.
func (c *Client) AlertsDisable(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ALERTS", "OFF")
	return cmd
}
