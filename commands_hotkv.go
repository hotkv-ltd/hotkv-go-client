// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"strconv"
	"time"
)

// --- Tag Commands (HotKV Exclusive) ---

// TagSet attaches one or more tags to an existing key.
func (c *Client) TagSet(ctx context.Context, key string, tags ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(tags))
	args = append(args, "TAG.SET", key)
	args = append(args, tags...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// TagRem removes one or more tags from a key.
func (c *Client) TagRem(ctx context.Context, key string, tags ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(tags))
	args = append(args, "TAG.REM", key)
	args = append(args, tags...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// TagGet returns all tags attached to a key.
func (c *Client) TagGet(ctx context.Context, key string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TAG.GET", key)
	return cmd
}

// TagMembers returns all keys that carry the given tag.
func (c *Client) TagMembers(ctx context.Context, tag string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TAG.MEMBERS", tag)
	return cmd
}

// TagInter returns keys that carry ALL of the given tags (intersection).
func (c *Client) TagInter(ctx context.Context, tags ...string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := make([]string, 0, 1+len(tags))
	args = append(args, "TAG.INTER")
	args = append(args, tags...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// TagCount returns the number of tags attached to a key.
func (c *Client) TagCount(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TAG.COUNT", key)
	return cmd
}

// TagExists returns whether the key has the given tag.
func (c *Client) TagExists(ctx context.Context, key string, tag string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TAG.EXISTS", key, tag)
	return cmd
}

// --- Nanosecond TTL Commands (HotKV Exclusive) ---

// NExpire sets key expiration in nanoseconds.
func (c *Client) NExpire(ctx context.Context, key string, nanoseconds int64) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "NEXPIRE", key, strconv.FormatInt(nanoseconds, 10))
	return cmd
}

// NTTL returns the remaining time to live in nanoseconds.
func (c *Client) NTTL(ctx context.Context, key string) *DurationCmd {
	cmd := NewDurationCmd(time.Nanosecond, nil)
	c.processCmd(ctx, &cmd.Cmd, "NTTL", key)
	return cmd
}

// NExpireAt sets key expiration at a nanosecond-precision UNIX timestamp.
func (c *Client) NExpireAt(ctx context.Context, key string, nsTimestamp int64) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "NEXPIREAT", key, strconv.FormatInt(nsTimestamp, 10))
	return cmd
}

// --- Monitoring Commands (HotKV Exclusive) ---

// HotKeysTop returns the hottest keys by access frequency.
func (c *Client) HotKeysTop(ctx context.Context, n int) *HotKeysCmd {
	cmd := NewHotKeysCmd(nil)
	val, err := c.process(ctx, "HOTKEYS", "TOP", strconv.Itoa(n))
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseHotKeys(cmd, val)
	return cmd
}

// HotKeysReset resets the hot key tracking counters.
func (c *Client) HotKeysReset(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HOTKEYS", "RESET")
	return cmd
}

// LatencyLatest returns the latest latency samples.
func (c *Client) LatencyLatest(ctx context.Context) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LATENCY", "LATEST")
	return cmd
}

// LatencyHistory returns the latency history for a command.
func (c *Client) LatencyHistory(ctx context.Context, event string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LATENCY", "HISTORY", event)
	return cmd
}

// LatencyReset resets latency data.
func (c *Client) LatencyReset(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LATENCY", "RESET")
	return cmd
}

// --- VSIM (Vector Similarity) Commands (HotKV Exclusive) ---

// VSIMIndexOptions configures the optional knobs of VSIM.CREATEINDEX.
// Zero values are omitted from the wire command, so the server applies its
// own defaults (METRIC cosine, M 16, EF_CONSTRUCTION 200, EF_SEARCH 50).
type VSIMIndexOptions struct {
	Metric         string // "cosine", "l2", or "ip"
	M              int
	EFConstruction int
	EFSearch       int
}

// VSIMSearchOptions configures the optional knobs of VSIM.SEARCH.
type VSIMSearchOptions struct {
	// EF overrides the index's default search-time ef parameter.
	EF int
	// Filter restricts results to keys carrying ALL of these tags
	// (via TAG.INTER). Sent before EF on the wire, as the server requires.
	Filter []string
}

// VSIMCreateIndex creates a vector similarity index with the given
// dimensionality. dims must be in 1..65536.
func (c *Client) VSIMCreateIndex(ctx context.Context, index string, dims int, opts *VSIMIndexOptions) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"VSIM.CREATEINDEX", index, strconv.Itoa(dims)}
	if opts != nil {
		if opts.Metric != "" {
			args = append(args, "METRIC", opts.Metric)
		}
		if opts.M > 0 {
			args = append(args, "M", strconv.Itoa(opts.M))
		}
		if opts.EFConstruction > 0 {
			args = append(args, "EF_CONSTRUCTION", strconv.Itoa(opts.EFConstruction))
		}
		if opts.EFSearch > 0 {
			args = append(args, "EF_SEARCH", strconv.Itoa(opts.EFSearch))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// VSIMSet adds or updates a vector in the index.
func (c *Client) VSIMSet(ctx context.Context, index string, key string, vector []float32) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := make([]string, 0, 3+len(vector))
	args = append(args, "VSIM.SET", index, key)
	for _, f := range vector {
		args = append(args, argFloat32(f))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// VSIMSearch searches for the k nearest vectors.
func (c *Client) VSIMSearch(ctx context.Context, index string, vector []float32, k int, opts *VSIMSearchOptions) *VSIMResultCmd {
	cmd := NewVSIMResultCmd(nil)
	args := make([]string, 0, 4+len(vector)+4)
	args = append(args, "VSIM.SEARCH", index, strconv.Itoa(k))
	for _, f := range vector {
		args = append(args, argFloat32(f))
	}
	if opts != nil {
		// FILTER must precede EF: the server reads filter tags greedily
		// until it sees the literal "EF" token.
		if len(opts.Filter) > 0 {
			args = append(args, "FILTER")
			args = append(args, opts.Filter...)
		}
		if opts.EF > 0 {
			args = append(args, "EF", strconv.Itoa(opts.EF))
		}
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseVSIMResults(cmd, val)
	return cmd
}

// VSIMGet returns the vector stored for a key in the index.
func (c *Client) VSIMGet(ctx context.Context, index string, key string) *Float32SliceCmd {
	cmd := NewFloat32SliceCmd(nil)
	val, err := c.process(ctx, "VSIM.GET", index, key)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseFloat32Slice(cmd, val)
	return cmd
}

// VSIMDel deletes a vector from the index.
func (c *Client) VSIMDel(ctx context.Context, index string, key string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.DEL", index, key)
	return cmd
}

// VSIMDropIndex drops a vector index and all its data.
func (c *Client) VSIMDropIndex(ctx context.Context, index string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.DROPINDEX", index)
	return cmd
}

// VSIMInfo returns metadata about a single vector index.
func (c *Client) VSIMInfo(ctx context.Context, index string) *VSIMInfoCmd {
	cmd := NewVSIMInfoCmd(nil)
	val, err := c.process(ctx, "VSIM.INFO", index)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseVSIMInfo(cmd, val)
	return cmd
}

// VSIMIndexNames returns the names of every vector index (VSIM.INFO with no
// index argument).
func (c *Client) VSIMIndexNames(ctx context.Context) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "VSIM.INFO")
	return cmd
}

// --- Parsers ---

func parseHotKeys(cmd *HotKeysCmd, val Value) {
	if val.IsNil() || val.Typ != RespArray {
		return
	}
	// The server replies with an array of [key, count] pairs; a flat
	// [key, count, key, count, ...] array is accepted too.
	pairs, err := valuePairSlice(val)
	if err != nil {
		cmd.SetErr(err)
		return
	}
	entries := make([]HotKeyEntry, 0, len(pairs))
	for _, p := range pairs {
		entries = append(entries, HotKeyEntry{Key: p[0].Str, Count: valInt(p[1])})
	}
	cmd.SetKeys(entries)
}

func parseVSIMResults(cmd *VSIMResultCmd, val Value) {
	if val.IsNil() || val.Typ != RespArray {
		return
	}
	var results []VSIMSearchResult
	for i := 0; i+1 < len(val.Elems); i += 2 {
		key := val.Elems[i].Str
		score := 0.0
		if val.Elems[i+1].Typ == RespDouble {
			score = val.Elems[i+1].Dbl
		} else {
			score, _ = strconv.ParseFloat(val.Elems[i+1].Str, 64)
		}
		results = append(results, VSIMSearchResult{
			Key:   key,
			Score: score,
		})
	}
	cmd.SetResults(results)
}

func parseFloat32Slice(cmd *Float32SliceCmd, val Value) {
	if val.IsNil() || val.Typ != RespArray {
		return
	}
	floats := make([]float32, 0, len(val.Elems))
	for _, e := range val.Elems {
		f, _ := strconv.ParseFloat(e.Str, 32)
		floats = append(floats, float32(f))
	}
	cmd.SetFloats(floats)
}

func vsimInfoInt(v Value) int64 {
	if v.Typ == RespInteger {
		return v.Int
	}
	n, _ := strconv.ParseInt(v.Str, 10, 64)
	return n
}

func applyVSIMInfoField(info *VSIMInfoResult, key string, v Value) {
	switch key {
	case "name":
		info.Name = v.Str
	case "dimensions":
		info.Dimensions = int(vsimInfoInt(v))
	case "metric":
		info.Metric = v.Str
	case "vectors":
		info.Vectors = vsimInfoInt(v)
	case "M":
		info.M = int(vsimInfoInt(v))
	case "ef_construction":
		info.EFConstruction = int(vsimInfoInt(v))
	case "ef_search":
		info.EFSearch = int(vsimInfoInt(v))
	}
}

// parseVSIMInfo decodes the flat 7-pair array (or RESP3 map) that
// VSIM.INFO <index> replies with: name, dimensions, metric, vectors, M,
// ef_construction, ef_search.
func parseVSIMInfo(cmd *VSIMInfoCmd, val Value) {
	if val.IsNil() {
		return
	}
	info := VSIMInfoResult{}
	if val.Typ == RespArray {
		for i := 0; i+1 < len(val.Elems); i += 2 {
			applyVSIMInfoField(&info, val.Elems[i].Str, val.Elems[i+1])
		}
	}
	if val.Typ == RespMap {
		for _, kv := range val.MapVal {
			applyVSIMInfoField(&info, kv.Key.Str, kv.Val)
		}
	}
	cmd.SetInfo(info)
}
