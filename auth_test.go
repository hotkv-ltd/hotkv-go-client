// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

// TestLiveAuth checks AUTH against a throwaway server started with
// requirepass. HOTKV_TEST_AUTH_URL is a URL carrying the password
// (hotkv://:password@host:port); HOTKV_TEST_AUTH_USER_URL optionally one for
// an ACL user (hotkv://user:password@host:port) that may only touch keys
// starting with "gosdk:".
func TestLiveAuth(t *testing.T) {
	raw := os.Getenv("HOTKV_TEST_AUTH_URL")
	if raw == "" {
		t.Skip("HOTKV_TEST_AUTH_URL not set (a server started with requirepass)")
	}
	ctx := context.Background()
	for _, proto := range []int{2, 3} {
		opts, err := hotkv.ParseURL(raw)
		ck(t, err, "ParseURL")
		opts.Protocol = proto
		c, _ := hotkv.NewClient(opts)
		if v, err := c.Ping(ctx).Result(); err != nil || v != "PONG" {
			t.Errorf("RESP%d: PING with the right password = %q, %v", proto, v, err)
		}
		ck(t, c.Set(ctx, "gosdk:auth", "ok", 0).Err(), "SET")
		c.Close()

		// wrong password
		bad, _ := hotkv.ParseURL(raw)
		bad.Protocol = proto
		bad.Password = "not-the-password"
		bad.MaxRetries = -1
		bc, _ := hotkv.NewClient(bad)
		err = bc.Ping(ctx).Err()
		var he *hotkv.Error
		if !errors.As(err, &he) || (he.Prefix() != "WRONGPASS" && he.Prefix() != "ERR") {
			t.Errorf("RESP%d: wrong password: %v, want a WRONGPASS server error", proto, err)
		}
		bc.Close()

		// no password at all
		anon, _ := hotkv.ParseURL(raw)
		anon.Protocol = proto
		anon.Password = ""
		anon.MaxRetries = -1
		ac, _ := hotkv.NewClient(anon)
		if err := ac.Get(ctx, "gosdk:auth").Err(); !hotkv.IsNoAuthError(err) {
			t.Errorf("RESP%d: no password: %v, want NOAUTH", proto, err)
		}
		ac.Close()
	}

	userRaw := os.Getenv("HOTKV_TEST_AUTH_USER_URL")
	if userRaw == "" {
		return
	}
	for _, proto := range []int{2, 3} {
		opts, err := hotkv.ParseURL(userRaw)
		ck(t, err, "ParseURL user")
		if opts.Username == "" {
			t.Fatal("HOTKV_TEST_AUTH_USER_URL has no user name")
		}
		opts.Protocol = proto
		c, _ := hotkv.NewClient(opts)
		ck(t, c.Set(ctx, "gosdk:acl", "v", 0).Err(), "ACL user SET on an allowed key")
		v, err := c.Get(ctx, "gosdk:acl").Result()
		ck(t, err, "ACL user GET")
		eq(t, "ACL user GET", v, "v")
		var he *hotkv.Error
		if err := c.Set(ctx, "forbidden-key", "v", 0).Err(); !errors.As(err, &he) || he.Prefix() != "NOPERM" {
			t.Errorf("RESP%d: ACL user SET on a forbidden key: %v, want NOPERM", proto, err)
		}
		c.Close()

		// the same user with a wrong password
		u, _ := url.Parse(userRaw)
		bad := *opts
		bad.Password = "nope"
		bad.MaxRetries = -1
		bc, _ := hotkv.NewClient(&bad)
		if err := bc.Ping(ctx).Err(); err == nil || strings.Contains(err.Error(), u.User.String()) {
			t.Errorf("RESP%d: ACL user with a wrong password: %v", proto, err)
		}
		bc.Close()
	}
}
