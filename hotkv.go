// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

// Package hotkv implements the official Go client for HotKV, a high-performance
// key-value store. It communicates using RESP2 and RESP3, implemented from
// scratch with zero third-party dependencies.
//
// Quick start:
//
//	client, err := hotkv.NewClient(&hotkv.Options{
//	    Addr: "localhost:6379",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
//	ctx := context.Background()
//	err = client.Set(ctx, "key", "value", 60*time.Second).Err()
//	val, err := client.Get(ctx, "key").Result()
package hotkv

// Version is the client library version.
const Version = "1.0.0"

// NewClient creates a new HotKV client with the given options.
// Connection is lazy: the first command triggers the initial connection.
func NewClient(opts *Options) (*Client, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.Addr == "" {
		opts.Addr = "localhost:6379"
	}
	return newClient(opts), nil
}
