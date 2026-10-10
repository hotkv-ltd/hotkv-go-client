// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PromptMessage is one message in a chat-style prompt, used only to build a
// deterministic PromptCache key. It has no dependency on any specific LLM
// provider's SDK or wire format.
type PromptMessage struct {
	Role    string
	Content string
}

// PromptCacheKeyInput is the normalized shape a PromptCache key is derived
// from. Params should be pre-formatted to strings by the caller (e.g.
// "0.7", not 0.7) so the same logical request always hashes to the same
// key regardless of language or float formatting.
type PromptCacheKeyInput struct {
	Provider string
	Model    string
	System   string
	Messages []PromptMessage
	Params   map[string]string
}

// BuildPromptCacheKey deterministically derives a cache key from in: a
// canonical, compact, sorted-key JSON encoding of the request, SHA-256
// hashed and returned as a lowercase hex string. Every official HotKV SDK
// implements the same canonical form, so keys are shareable across
// languages.
func BuildPromptCacheKey(in PromptCacheKeyInput) string {
	var b strings.Builder
	b.WriteByte('{')

	b.WriteString(`"provider":`)
	appendJSONString(&b, in.Provider)
	b.WriteByte(',')

	b.WriteString(`"model":`)
	appendJSONString(&b, in.Model)
	b.WriteByte(',')

	b.WriteString(`"system":`)
	appendJSONString(&b, in.System)
	b.WriteByte(',')

	b.WriteString(`"messages":[`)
	for i, m := range in.Messages {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('{')
		b.WriteString(`"role":`)
		appendJSONString(&b, m.Role)
		b.WriteString(`,"content":`)
		appendJSONString(&b, m.Content)
		b.WriteByte('}')
	}
	b.WriteString(`],`)

	b.WriteString(`"params":{`)
	keys := make([]string, 0, len(in.Params))
	for k := range in.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		appendJSONString(&b, k)
		b.WriteByte(':')
		appendJSONString(&b, in.Params[k])
	}
	b.WriteByte('}')

	b.WriteByte('}')

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// appendJSONString writes s to b as a compact, standard-escaped JSON string
// literal (including the surrounding quotes). Non-ASCII runes are written
// as literal UTF-8, matching json.dumps(..., ensure_ascii=False).
func appendJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// LLMUsage records token/cost/latency accounting for one LLM call, returned
// by the caller's model function on a PromptCache/SemanticCache miss.
// Model, when non-empty, does two things: it enables LLMSTATS.RECORD-based
// accounting (WithStatsBucket) to price the call from the server's price
// table, and it is attached to the stored cache entry (the PCACHE.SET/
// SCACHE.SET usage group, see Usage) so a later hit attributes the tokens
// and cost that hit did not have to spend.
type LLMUsage struct {
	Model              string
	InputTokens        uint64
	OutputTokens       uint64
	CacheReadTokens    uint64
	CacheWrite5mTokens uint64
	CacheWrite1hTokens uint64
	CostUSD            float64
	Latency            time.Duration
}

// asUsage returns u as a cache-entry Usage group, or nil when u has no
// Model (Usage requires one).
func (u LLMUsage) asUsage() *Usage {
	if u.Model == "" {
		return nil
	}
	return &Usage{
		Model:        u.Model,
		Input:        u.InputTokens,
		Output:       u.OutputTokens,
		CacheRead:    u.CacheReadTokens,
		CacheWrite5m: u.CacheWrite5mTokens,
		CacheWrite1h: u.CacheWrite1hTokens,
	}
}

// LLMCallResult is what the caller-supplied function passed to
// PromptCache.GetOrCall must return on a miss.
type LLMCallResult struct {
	Response string
	Usage    LLMUsage
}

// PromptCache is an idiomatic get-or-call cache in front of PCACHE.*: on a
// cache hit it returns the stored response without invoking the LLM; on a
// miss it calls the caller's function, stores the result, and returns it.
// PromptCache needs a server license that includes the prompt-cache
// feature; a NOLICENSE error surfaces from GetOrCall like any other server
// error.
type PromptCache struct {
	client      *Client
	ns          string
	ttl         time.Duration
	statsBucket string // optional; empty disables LLMSTATS.RECORD calls
}

// NewPromptCache returns a PromptCache that stores entries in namespace ns
// with the given default TTL (0 = no expiry). Call CreateNamespace once
// before first use (idempotent to call again).
func NewPromptCache(client *Client, ns string, ttl time.Duration) *PromptCache {
	return &PromptCache{client: client, ns: ns, ttl: ttl}
}

// WithStatsBucket enables LLMSTATS.RECORD accounting on every GetOrCall
// (hit or miss) against the named bucket, returning pc for chaining. The
// bucket is created (LLMSTATS.BUCKET) automatically on first use.
func (pc *PromptCache) WithStatsBucket(bucket string) *PromptCache {
	pc.statsBucket = bucket
	return pc
}

// CreateNamespace ensures the PCACHE namespace backing this PromptCache
// exists. maxEntries 0 uses the engine default; costPerHit records the
// per-hit savings PCACHE.STATS reports.
func (pc *PromptCache) CreateNamespace(ctx context.Context, maxEntries uint64, costPerHit float64) error {
	err := pc.client.PCacheCreate(ctx, pc.ns, maxEntries, pc.ttl, costPerHit).Err()
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// GetOrCall returns the cached response for in if present, otherwise calls
// fn, caches its response, and returns that. hit reports whether the
// response came from cache.
func (pc *PromptCache) GetOrCall(ctx context.Context, in PromptCacheKeyInput, fn func(context.Context) (LLMCallResult, error)) (response string, hit bool, err error) {
	key := BuildPromptCacheKey(in)
	start := time.Now()

	if v, gerr := pc.client.PCacheGet(ctx, pc.ns, key).Result(); gerr == nil {
		pc.record(ctx, LLMUsage{Latency: time.Since(start)}, true)
		return v, true, nil
	} else if gerr != Nil && !IsNoLicenseError(gerr) {
		return "", false, gerr
	} else if IsNoLicenseError(gerr) {
		// Fall through to calling fn: the cache is unavailable, not the LLM.
	}

	result, cerr := fn(ctx)
	if cerr != nil {
		return "", false, cerr
	}

	_ = pc.client.PCacheSet(ctx, pc.ns, key, result.Response, pc.ttl, "", result.Usage.asUsage()).Err()
	pc.record(ctx, result.Usage, false)

	return result.Response, false, nil
}

// Stats returns the current PCACHE.STATS for this cache's namespace,
// including how many tokens and how much cost recorded usage has saved on
// hits (see UsageSavings).
func (pc *PromptCache) Stats(ctx context.Context) (PCacheStatsResult, error) {
	return pc.client.PCacheStats(ctx, pc.ns).Result()
}

func (pc *PromptCache) record(ctx context.Context, usage LLMUsage, hit bool) {
	if pc.statsBucket == "" {
		return
	}
	input, output, cached, cost := usage.InputTokens, usage.OutputTokens, uint64(0), usage.CostUSD
	cw5m, cw1h := usage.CacheWrite5mTokens, usage.CacheWrite1hTokens
	if hit {
		cached, input, output, cost, cw5m, cw1h = input+output, 0, 0, 0, 0, 0
	}
	opts := &LLMStatsRecordOptions{Model: usage.Model, CacheWrite5m: cw5m, CacheWrite1h: cw1h}
	_ = pc.client.LLMStatsRecord(ctx, pc.statsBucket, input, output, cached, cost, usage.Latency, opts).Err()
}

// SemanticCache is an idiomatic get-or-call cache in front of SCACHE.*: it
// looks up the nearest cached answer to an embedding vector above the
// namespace's similarity threshold, calling the caller's function only on
// a miss. It never computes embeddings itself; callers supply vectors from
// whatever embedding provider they use. SemanticCache needs
// a server license that includes the semantic-cache feature.
type SemanticCache struct {
	client *Client
	ns     string
	ttl    time.Duration
}

// NewSemanticCache returns a SemanticCache backed by namespace ns, with the
// given default TTL for stored entries (0 = no expiry). Call
// CreateNamespace once before first use.
func NewSemanticCache(client *Client, ns string, ttl time.Duration) *SemanticCache {
	return &SemanticCache{client: client, ns: ns, ttl: ttl}
}

// CreateNamespace ensures the SCACHE namespace backing this SemanticCache
// exists, with the given embedding dimensionality, distance metric
// ("cosine", "l2", or "ip"; "" defaults to cosine) and similarity
// threshold (nil uses the engine default, 0.85).
func (sc *SemanticCache) CreateNamespace(ctx context.Context, dims int, metric string, threshold *float64) error {
	err := sc.client.SCacheCreate(ctx, sc.ns, dims, &SCacheCreateOptions{
		Metric: metric, Threshold: threshold, TTL: sc.ttl,
	}).Err()
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// GetOrCall returns the cached answer for the nearest neighbor of vector
// above the namespace's similarity threshold, if any; otherwise it calls
// fn, caches (query, response, vector) together with any usage fn reports
// (Model required for it to be attached; see LLMUsage), and returns the
// fresh answer.
func (sc *SemanticCache) GetOrCall(ctx context.Context, query string, vector []float32, fn func(context.Context) (LLMCallResult, error)) (response string, hit bool, err error) {
	res, gerr := sc.client.SCacheGet(ctx, sc.ns, vector, nil).Result()
	if gerr != nil && !IsNoLicenseError(gerr) {
		return "", false, gerr
	}
	if res.Hit {
		return res.Response, true, nil
	}

	result, cerr := fn(ctx)
	if cerr != nil {
		return "", false, cerr
	}

	_ = sc.client.SCacheSet(ctx, sc.ns, query, result.Response, vector, sc.ttl, result.Usage.asUsage()).Err()
	return result.Response, false, nil
}

// Stats returns the current SCACHE.STATS for this cache's namespace,
// including how many tokens and how much cost recorded usage has saved on
// hits (see UsageSavings).
func (sc *SemanticCache) Stats(ctx context.Context) (SCacheStatsResult, error) {
	return sc.client.SCacheStats(ctx, sc.ns).Result()
}
