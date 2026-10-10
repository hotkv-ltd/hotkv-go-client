// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"fmt"
	"log"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

func ExampleNewClient() {
	client, err := hotkv.NewClient(&hotkv.Options{
		Addr: "localhost:6379",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()

	// SET and GET
	err = client.Set(ctx, "greeting", "hello", 0).Err()
	if err != nil {
		log.Fatal(err)
	}

	val, err := client.Get(ctx, "greeting").Result()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(val)
	// Would print: hello (requires a live server; see the package doc
	// comment and README for how to run examples against HOTKV_TEST_URL).
}

func ExampleClient_Set() {
	client, _ := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	defer client.Close()
	ctx := context.Background()

	// Simple set.
	client.Set(ctx, "key", "value", 0)

	// Set with expiry.
	client.Set(ctx, "key", "value", 60*time.Second)

	// Set if not exists.
	client.SetNX(ctx, "key", "value", 0)
}

func ExampleClient_Pipeline() {
	client, _ := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	defer client.Close()
	ctx := context.Background()

	pipe := client.Pipeline()
	pipe.Set(ctx, "key1", "val1")
	pipe.Set(ctx, "key2", "val2")
	get1 := pipe.Get(ctx, "key1")
	get2 := pipe.Get(ctx, "key2")

	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(get1.Val())
	fmt.Println(get2.Val())
}

func ExampleClient_TxPipeline() {
	client, _ := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	defer client.Close()
	ctx := context.Background()

	pipe := client.TxPipeline()
	pipe.Incr(ctx, "counter")
	pipe.Incr(ctx, "counter")

	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleClient_Subscribe() {
	client, _ := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	defer client.Close()
	ctx := context.Background()

	sub := client.Subscribe(ctx, "notifications")
	defer sub.Close()

	ch := sub.Channel()
	for msg := range ch {
		fmt.Println(msg.Channel, msg.Payload)
	}
}

func ExampleClient_TagSet() {
	client, _ := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	defer client.Close()
	ctx := context.Background()

	client.Set(ctx, "user:1001", `{"name":"Alice"}`, 0)

	// Attach tags.
	client.TagSet(ctx, "user:1001", "premium", "active")

	// Query keys by tag.
	keys, _ := client.TagMembers(ctx, "premium").Result()
	fmt.Println(keys)
}

func ExampleClient_VSIMCreateIndex() {
	client, _ := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	defer client.Close()
	ctx := context.Background()

	// Create a vector index.
	client.VSIMCreateIndex(ctx, "embeddings", 128, &hotkv.VSIMIndexOptions{
		Metric: "cosine",
	})

	// Add vectors.
	vec := make([]float32, 128)
	client.VSIMSet(ctx, "embeddings", "doc:1", vec)

	// Search.
	results, _ := client.VSIMSearch(ctx, "embeddings", vec, 10, nil).Result()
	for _, r := range results {
		fmt.Println(r.Key, r.Score)
	}
}

func ExampleClient_Do() {
	client, err := hotkv.NewClient(&hotkv.Options{Addr: "localhost:6379"})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()

	// Any command without a typed method goes through Do.
	if err := client.Do(ctx, "BF.RESERVE", "seen", 0.01, 1000).Err(); err != nil {
		log.Fatal(err)
	}
	added, _ := client.Do(ctx, "BF.ADD", "seen", "user:1").Bool()
	left, _ := client.Do(ctx, "HGETDEL", "h", "FIELDS", 1, "f").StringSlice()
	dist, _ := client.Do(ctx, "GEODIST", "sicily", "Palermo", "Catania", "km").Float64()
	fmt.Println(added, left, dist)
}

func ExampleNewClusterClient() {
	cc, err := hotkv.NewClusterClient(&hotkv.ClusterOptions{
		Addrs:    []string{"10.0.0.1:6379", "10.0.0.2:6379"},
		Options:  hotkv.Options{Password: "secret", PoolSize: 20},
		ReadOnly: false,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer cc.Close()
	ctx := context.Background()

	// Routed by CRC16 of the {hash tag}.
	if err := cc.Set(ctx, "user:{42}:name", "Ada", 0).Err(); err != nil {
		log.Fatal(err)
	}
	name, _ := cc.Get(ctx, "user:{42}:name").Result()
	fmt.Println(name)

	// Commands that act on one shard run on each primary.
	_ = cc.ForEachPrimary(ctx, func(ctx context.Context, n *hotkv.Client) error {
		return n.FlushDB(ctx).Err()
	})
}
