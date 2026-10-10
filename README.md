# HotKV Go Client

[![CI](https://github.com/hotkv-ltd/hotkv-go-client/actions/workflows/ci.yml/badge.svg)](https://github.com/hotkv-ltd/hotkv-go-client/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

A high-performance Go client for [HotKV](https://hotkv.com), a RESP2/RESP3-compatible
in-memory data store with built-in AI and LLM-serving features (semantic and prompt
caching, agent memory, RAG, feature store, rate limiting, time series and more).

- RESP2 and RESP3 implemented from scratch (zero third-party dependencies)
- Connection pooling with configurable limits and idle reaping
- Retry with exponential backoff and jitter
- TLS support, and connection URL parsing (`hotkv://`, `hotkvs://`, `redis://`, `rediss://`)
- Typed methods for the common data-structure commands (strings, keys, lists,
  sets, hashes, sorted sets, stream basics, bitmaps, HyperLogLog, Pub/Sub,
  transactions, server) and for the HotKV-exclusive and enterprise/AI
  families (Tags, Nanosecond TTL, Vector Similarity, semantic and prompt
  caching, agent memory, RAG, rate limiting, time series, feature store, and
  more): 178 of 429 core commands and 144 of 177 enterprise commands
- `Do` runs any other command (GEO, scripting, newer hash and string
  commands, Bloom/Count-Min/Top-K, CLIENT TRACKING, CLUSTER, ...) with typed
  reply accessors, and works in pipelines and on the cluster client
- `ClusterClient`: slot map from `CLUSTER SLOTS`, CRC16 hash slots with
  `{hash tags}`, `MOVED`/`ASK` handling, refresh on topology change, per-node
  pools, replica reads, node-grouped pipelines
- `PromptCache` and `SemanticCache` helpers: deterministic cache-key building
  and a get-or-call wrapper so an LLM call only happens on a miss
- Cluster key slot calculation (`KeySlot`, CRC16 with `{hash tag}` support)
- Pipelining and MULTI/EXEC transactions
- Pub/Sub with channel-based delivery
- Fluent result types (`StringCmd`, `IntCmd`, `BoolCmd`, etc.)

## Requirements

- Go 1.21 or later
- A HotKV server

## Install

```bash
go get github.com/hotkv-ltd/hotkv-go-client
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

func main() {
    client, err := hotkv.NewClient(&hotkv.Options{
        Addr: "localhost:6379",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    ctx := context.Background()

    // SET with 60-second expiry
    err = client.Set(ctx, "key", "value", 60*time.Second).Err()
    if err != nil {
        log.Fatal(err)
    }

    // GET
    val, err := client.Get(ctx, "key").Result()
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(val) // "value"
}
```

## Configuration

```go
client, err := hotkv.NewClient(&hotkv.Options{
    Addr:         "localhost:6379",
    Password:     "secret",
    DB:           0,
    Protocol:     3,             // RESP3 (default)
    PoolSize:     10,
    MinIdleConns: 2,
    DialTimeout:  5 * time.Second,
    ReadTimeout:  3 * time.Second,
    WriteTimeout: 3 * time.Second,
    MaxRetries:   3,
    TLSConfig:    &tls.Config{}, // nil for plain TCP
})
```

## Pipelines

```go
pipe := client.Pipeline()
pipe.Set(ctx, "k1", "v1")
pipe.Set(ctx, "k2", "v2")
get := pipe.Get(ctx, "k1")

_, err := pipe.Exec(ctx)
// get.Val() is available after Exec
```

## Transactions

```go
pipe := client.TxPipeline()
pipe.Incr(ctx, "counter")
pipe.Incr(ctx, "counter")
_, err := pipe.Exec(ctx)
// Both INCRs are atomic via MULTI/EXEC
```

## Pub/Sub

```go
sub := client.Subscribe(ctx, "events")
defer sub.Close()

for msg := range sub.Channel() {
    fmt.Println(msg.Channel, msg.Payload)
}
```

## Any command: `Do`

Commands without a typed method go through `Do`. Arguments may be strings, `[]byte`, integers,
floats, booleans, `fmt.Stringer` or `encoding.BinaryMarshaler` values; any
other type is an error and nothing is sent. The reply has typed accessors
(`Text`, `Int64`, `Float64`, `Bool`, `Slice`, `StringSlice`, `Result`, or the
raw `Val`):

```go
ctx := context.Background()
client.Do(ctx, "BF.RESERVE", "seen", 0.01, 1000)
added, _ := client.Do(ctx, "BF.ADD", "seen", "user:1").Bool()

client.Do(ctx, "HSETEX", "h", "EX", 100, "FIELDS", 1, "f", "v")
deleted, _ := client.Do(ctx, "HGETDEL", "h", "FIELDS", 1, "f").StringSlice()

dist, _ := client.Do(ctx, "GEODIST", "sicily", "Palermo", "Catania", "km").Float64()
```

A null reply is not an error on the command itself; the accessors report it
as `hotkv.Nil`. `Pipeline.Do` queues the same way. The command families that
have no typed method today are, among others: `BF.*`, `CMS.*`, `TOPK.*`,
`GEO*`, `EVAL*`/`SCRIPT`/`FUNCTION`/`FCALL`, `CLIENT TRACKING`/`CACHING`/
`GETREDIR`/`TRACKINGINFO`/`KILL`/`PAUSE`, `CLUSTER *`, `ACL *`, stream
consumer groups (`XGROUP`, `XREADGROUP`, `XACK`, `XPENDING`, `XCLAIM`),
hash field expiry (`HEXPIRE`, `HTTL`, ...), the newer commands `HGETDEL`,
`HGETEX`, `HSETEX`, `MSETEX`, `DELEX`, `HIMPORT`, `XCFGSET`, `SFLUSH`,
`CLUSTERSCAN`, `TRIMSLOTS`, the list moves (`LMOVE`, `LPOS`, `LTRIM`, ...),
the sorted-set range and algebra commands beyond the basics (`ZRANGEBYLEX`,
`ZUNION`, `ZMPOP`, ...), `OBJECT FREQ`/`IDLETIME`, `DUMP`/`RESTORE`, and the
enterprise `TENANT.*`, `IAM.*`, `AUDIT.*`, `ENCRYPTION.*`, `REPLICATION.*`
and `FEATURE.SETAT` families.

### Numbers on the wire

Floats are always written as plain decimals with the shortest digits that
round-trip: never an exponent (`1e-05`), never a leading `+`. NaN is
rejected everywhere and so is infinity, except in sorted-set scores and
weights, which send `inf`/`-inf`. The strict fixed-point USD fields
(`LLMBudgetSet`'s cost limit, `LLMStatsPriceSet`'s prices) accept
non-negative amounts with at most six decimals, round to micro-dollars, and
refuse an amount that rounds to zero (zero means "no limit" for a budget).
A value that cannot be sent makes the call fail with an error before
anything reaches the server.

## Cluster

```go
cc, err := hotkv.NewClusterClient(&hotkv.ClusterOptions{
    Addrs:    []string{"10.0.0.1:6379", "10.0.0.2:6379"}, // any reachable seeds
    Options:  hotkv.Options{Password: "secret", PoolSize: 20}, // per node
    ReadOnly: false, // true: reads may be served by replicas
})
defer cc.Close()

cc.Set(ctx, "user:{42}:name", "Ada", 0)    // routed by CRC16 of "42"
cc.Get(ctx, "user:{42}:name")               // same slot, same node
cc.Do(ctx, "BF.ADD", "{seen}", "x")         // any command is routed by its keys

// run something on every shard
cc.ForEachPrimary(ctx, func(ctx context.Context, n *hotkv.Client) error {
    return n.FlushDB(ctx).Err()
})
```

`ClusterClient` embeds `*Client`, so every typed method and `Do` routes
automatically. Key positions come from the server's own `COMMAND` key
specifications, so new server commands route correctly without an SDK
release. Multi-key commands must keep their keys in one slot (use a
`{hash tag}`); otherwise the server answers `CROSSSLOT`. Pipelines are split
by owning node and run in parallel; a `TxPipeline` and `Watch` need all keys
in one slot. `MOVED` updates the slot owner and retries, `ASK` sends
`ASKING` to the target first, and both are bounded by `MaxRedirects`
(default 5); a failed node or `CLUSTERDOWN` reloads the slot map.

## HotKV exclusive commands

### Tags

```go
client.Set(ctx, "user:1001", `{"name":"Alice"}`, 0)
client.TagSet(ctx, "user:1001", "premium", "active")

keys, _ := client.TagMembers(ctx, "premium").Result()
exists, _ := client.TagExists(ctx, "user:1001", "premium").Result()
```

### Nanosecond TTL

```go
client.NExpire(ctx, "key", 500_000_000) // 500ms in nanoseconds
ttl, _ := client.NTTL(ctx, "key").Result()
```

### Vector Similarity (VSIM)

```go
client.VSIMCreateIndex(ctx, "embeddings", 128, &hotkv.VSIMIndexOptions{
    Metric: "cosine",
})

client.VSIMSet(ctx, "embeddings", "doc:1", vector)
results, _ := client.VSIMSearch(ctx, "embeddings", queryVec, 10, nil).Result()
info, _ := client.VSIMInfo(ctx, "embeddings").Result()
```

### Hot Keys Monitoring

```go
top, _ := client.HotKeysTop(ctx, 10).Result()
for _, entry := range top {
    fmt.Println(entry.Key, entry.Count)
}
```

## Enterprise and AI commands

Every HotKV enterprise/AI command family has a typed method on `*Client`:
`SCacheCreate`/`SCacheSet`/`SCacheGet`/... (semantic cache), `PCacheCreate`/...
(prompt cache), `ToolCacheCreate`/`NCacheCreate`/`GuardCacheCreate`/`RerankCacheCreate`
and their `Set`/`Get`/`Del`/`Stats`/`Flush`/`Drop`/`List` siblings (keyed caches),
`LLMStats*` (including `LLMStatsPriceSet`/`Get`/`List`/`Del`), `LLMBudget*`,
`Prompt*` (prompt registry), `EmbedDedup*`, `Amem*` (conversation, semantic,
working and episodic agent memory, cross-memory search, and
`AmemSummary*`), `RAG*`, `RateLimit*`, `TS*` (time series), `Feature*`
(feature store), `Observe*`,
`Alerts*`, and `GCRALimit`/`GCRAReset`. Enterprise commands need a licensed
HotKV server; `hotkv.IsNoLicenseError(err)` detects the
`NOLICENSE` error an unlicensed server returns (see also `IsMovedError`,
`IsAskError`, `IsWrongTypeError`, `IsNoAuthError`, `IsReadOnlyError`).

Every `SET` in the cache families above (`PCacheSet`, `SCacheSet`,
`ToolCacheSet`, `NCacheSet`, `GuardCacheSet`, `RerankCacheSet`) and
`LLMBudgetConsume` take an optional trailing `*hotkv.Usage` (model required,
token counts default to 0): recording the original call's usage lets a
later cache hit attribute the tokens, and once the model is priced the
cost, it did not have to spend. Their `Stats`-family replies
(`PCacheStats`, `SCacheStats`, the keyed caches' `Stats`) expose those
savings as `Usage.CostUSD` and friends, alongside the existing counters.

### AI caching example

`PromptCache` wraps PCACHE.\* with a deterministic, cross-SDK cache key and a
get-or-call helper so you only pay for an LLM call on a miss. Reporting
`Model` in the returned usage attaches it to the stored entry, so
`pc.Stats(ctx)` reports the tokens and cost a later hit saved:

```go
pc := hotkv.NewPromptCache(client, "chatbot", time.Hour)
pc.WithStatsBucket("chatbot-usage") // optional: records hits/misses via LLMSTATS
_ = pc.CreateNamespace(ctx, 0, 0)   // 0, 0 = engine defaults

in := hotkv.PromptCacheKeyInput{
    Provider: "openai",
    Model:    "gpt-4",
    Messages: []hotkv.PromptMessage{{Role: "user", Content: "What is the capital of France?"}},
    Params:   map[string]string{"temperature": "0.0"},
}

response, hit, err := pc.GetOrCall(ctx, in, func(ctx context.Context) (hotkv.LLMCallResult, error) {
    // Call your LLM provider of choice here; this only runs on a cache miss.
    return hotkv.LLMCallResult{
        Response: "Paris is the capital of France.",
        Usage:    hotkv.LLMUsage{Model: "gpt-4", InputTokens: 12, OutputTokens: 8, CostUSD: 0.0003},
    }, nil
})
fmt.Println(response, hit, err)

stats, _ := pc.Stats(ctx)
fmt.Println("tokens saved so far:", stats.Usage.InputTokens+stats.Usage.OutputTokens)
```

`SemanticCache` is the equivalent wrapper around SCACHE.\*, keyed by an
embedding vector you supply (it never computes embeddings itself); its
callback returns the same `hotkv.LLMCallResult` shape as `PromptCache`'s:

```go
sc := hotkv.NewSemanticCache(client, "faq", 0)
_ = sc.CreateNamespace(ctx, 1536, "cosine", nil)

response, hit, err := sc.GetOrCall(ctx, "capital of france?", queryEmbedding,
    func(ctx context.Context) (hotkv.LLMCallResult, error) {
        // only called on a miss
        return hotkv.LLMCallResult{
            Response: "Paris.",
            Usage:    hotkv.LLMUsage{Model: "gpt-4", InputTokens: 6, OutputTokens: 1},
        }, nil
    })
```

### LLM cost tracking

`LLMSTATS.RECORD` with `MODEL` prices a call from HotKV's built-in price
table instead of a caller-supplied `costUSD`; `LLMSTATS.PRICE SET` adds or
overrides a model's price, and `LLMBUDGET.CONSUME` with `MODEL` enforces a
budget priced the same way:

```go
// Custom or fine-tuned model: 3 USD / 1M input tokens, 15 USD / 1M output.
_ = client.LLMStatsPriceSet(ctx, "acme-custom-v1", 3, 15, nil).Err()

_ = client.LLMStatsBucket(ctx, "chatbot-usage").Err()
_ = client.LLMStatsRecord(ctx, "chatbot-usage", 500, 120, 0, 0, 340*time.Millisecond,
    &hotkv.LLMStatsRecordOptions{Model: "acme-custom-v1"}).Err()

usage, _ := client.LLMStatsGet(ctx, "chatbot-usage").Result()
fmt.Printf("spent $%.6f so far\n", usage.CostUSD)

allowed, _ := client.LLMBudgetConsume(ctx, "chatbot-daily", 0, 0,
    &hotkv.Usage{Model: "acme-custom-v1", Input: 500, Output: 120}).Result()
if !allowed {
    log.Println("daily LLM budget exceeded")
}
```

## Command coverage

Typed methods exist for these command families. Everything else is reachable
through `Do`.

| Category            | Typed methods for |
| ------------------- | ----------------- |
| String              | SET (NX/XX/EX/PX), GET, GETSET, GETDEL, GETEX, GETRANGE, SETRANGE, MSET, MSETNX, MGET, INCR/DECR family, APPEND, STRLEN |
| Key                 | DEL, UNLINK, EXISTS, EXPIRE family, TTL family, PERSIST, TYPE, RENAME, COPY, KEYS, SCAN, RANDOMKEY, TOUCH, SORT, OBJECT ENCODING, MEMORY USAGE |
| List                | LPUSH, RPUSH, LPUSHX, RPUSHX, LPOP, RPOP, LLEN, LRANGE, LINDEX, LSET, LREM, LINSERT, BLPOP, BRPOP |
| Set                 | SADD, SREM, SMEMBERS, SISMEMBER, SMISMEMBER, SCARD, SPOP, SRANDMEMBER, SUNION/SINTER/SDIFF (+STORE), SSCAN |
| Hash                | HSET, HGET, HDEL, HEXISTS, HLEN, HKEYS, HVALS, HGETALL, HSETNX, HINCRBY, HINCRBYFLOAT, HMGET, HSCAN, HRANDFIELD |
| Sorted set          | ZADD (NX/XX/GT/LT/CH), ZREM, ZSCORE, ZRANK, ZREVRANK, ZCARD, ZRANGE, ZREVRANGE, ZRANGEBYSCORE, ZREVRANGEBYSCORE, ZCOUNT, ZINCRBY, ZPOPMIN/MAX, ZUNIONSTORE, ZINTERSTORE, ZRANDMEMBER, ZSCAN |
| Stream              | XADD, XLEN, XRANGE, XREVRANGE, XDEL, XTRIM, XREAD, XINFO STREAM (no consumer groups) |
| Bitmap, HyperLogLog | SETBIT, GETBIT, BITCOUNT, BITPOS, BITOP; PFADD, PFCOUNT, PFMERGE |
| Pub/Sub             | PUBLISH, SUBSCRIBE/PSUBSCRIBE (+UN), PUBSUB CHANNELS/NUMSUB/NUMPAT |
| Transactions        | MULTI/EXEC (`TxPipeline`), WATCH/UNWATCH (`Watch`) |
| Server              | PING, ECHO, INFO, DBSIZE, FLUSHDB, FLUSHALL, SAVE, BGSAVE, BGREWRITEAOF, LASTSAVE, TIME, CONFIG GET/SET/RESETSTAT, CLIENT ID/GETNAME/SETNAME/LIST, SELECT, SWAPDB, SHUTDOWN, SLOWLOG, LATENCY |
| Tag (HotKV)         | TAG.SET, TAG.GET, TAG.REM, TAG.MEMBERS, TAG.INTER, TAG.COUNT, TAG.EXISTS |
| TTL (HotKV)         | NEXPIRE, NTTL, NEXPIREAT |
| VSIM (HotKV)        | VSIM.CREATEINDEX, SET, GET, DEL, SEARCH, INFO, DROPINDEX; persistence SAVE/LOAD/LIST/DROP |
| Rate limit (HotKV)  | GCRA.LIMIT/RESET; RATELIMIT.CREATE/CHECK/PEEK/RESET/STATS/DROP/LIST |
| Monitor (HotKV)     | HOTKEYS TOP/RESET, OBSERVE INFO/METRICS, ALERTS |
| AI caching          | SCACHE.\*, PCACHE.\*, TOOL.CACHE.\*, NCACHE.\*, GUARD.\*, RERANK.\* |
| LLM ops             | LLMSTATS.\* (+PRICE), LLMBUDGET.\*, PROMPT.\*, EMBED.DEDUP.\* |
| Agent memory        | AMEM.\* (conversation, semantic, working, episodic, summary, search) |
| RAG, data           | RAG.\*, TS.\*, FEATURE.\* (not FEATURE.SETAT) |

Not typed (use `Do`): see the list under "Any command: `Do`". `Select` and
`ClientSetName` change the state of one pooled connection; use a client with
`PoolSize: 1` (or `Client.Conn`) when you need them to stick.

## Error handling

Server errors such as `WRONGTYPE`, `MOVED` or `NOLICENSE` come back as a
`*hotkv.Error`; `Prefix()`, `Message()` and `Raw()` expose its parts, and the helpers
`IsMovedError`, `IsAskError`, `IsReadOnlyError`, `IsNoAuthError`, `IsWrongTypeError` and
`IsNoLicenseError` classify it:

```go
_, err := client.Get(ctx, "missing").Result()
switch {
case errors.Is(err, hotkv.Nil):
    // a null reply: the key does not exist
case hotkv.IsWrongTypeError(err):
    // the key holds a different type
case err != nil:
    // a network or server error
}
```

`hotkv.Nil` stands for a null reply, `hotkv.TxFailedErr` for an `EXEC` that was aborted because a
watched key changed, and `hotkv.ErrClosed`, `hotkv.ErrPoolTimeout` and `hotkv.ErrPoolExhausted`
for a closed client or a pool that could not hand out a connection.

## Testing

Unit tests need no server and are what CI runs:

```bash
go build ./... && go vet ./... && go test -race ./...
```

The live tests call every typed method against a real server and check the
reply values, on RESP2 and RESP3. They are skipped unless `HOTKV_TEST_URL` is
set; the enterprise and AI families need a licensed server (without one
those subtests are reported as skipped):

```bash
HOTKV_TEST_URL=hotkv://127.0.0.1:6379 go test -count=1 -v ./...
```

Extra gates for tests that touch more than their own keys:

| Variable | Enables |
| --- | --- |
| `HOTKV_TEST_ALLOW_FLUSH=1` | the `FLUSHALL` test |
| `HOTKV_TEST_SHUTDOWN_URLS=u1,u2,u3` | `SHUTDOWN`, `SHUTDOWN SAVE`, `SHUTDOWN NOSAVE` against three throwaway servers |
| `HOTKV_TEST_ALLOW_ALERTS=1` | `ALERTS URL/ENABLE/TEST/DISABLE` |
| `HOTKV_TEST_AUTH_URL`, `HOTKV_TEST_AUTH_USER_URL` | AUTH against a server started with `requirepass`, and an ACL user |
| `HOTKV_REMOTE_HOST`, `_PORT`, `_PASSWORD`, `_CA` | the remote TLS + password suite (`TestRemote`) |

The cluster client is unit-tested against an in-process fake cluster
(`MOVED`, `ASK`, `TRYAGAIN`, node failure, replica reads, pipelines,
transactions); it has not been run against a live multi-node cluster.

Enterprise/AI commands on a server that is not licensed for them reply with a
`NOLICENSE` error; `hotkv.IsNoLicenseError(err)` detects it.

## Contributing

Bug reports and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Support

- Bugs and feature requests: [GitHub issues](https://github.com/hotkv-ltd/hotkv-go-client/issues)
- Questions and commercial support: support@hotkv.com
- Security vulnerabilities: see [SECURITY.md](SECURITY.md)

## License

Licensed under the Apache License, Version 2.0: see [LICENSE](LICENSE) and [NOTICE](NOTICE).

## Trademarks and affiliation

"HotKV" is a trademark of HotKV Ltd; the license does not grant rights to use it.
The HotKV server is a separate commercial product, and its enterprise commands need a HotKV license.

HotKV is an independent product of HotKV Ltd. It speaks the RESP protocol and implements
many Redis commands so that existing tools and client habits carry over, but it is not Redis,
Valkey, Dragonfly or KeyDB, and this SDK is built for and tested against HotKV. HotKV Ltd is not
affiliated with, endorsed by or sponsored by Redis Ltd., the Valkey project, DragonflyDB or KeyDB.
Redis is a registered trademark of Redis Ltd. Valkey, Dragonfly, KeyDB and all other product and
company names are trademarks of their respective owners; they are used here only to describe
protocol and command compatibility.
