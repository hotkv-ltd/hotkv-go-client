// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

// The live_*_test.go files call every typed method against a real server
// and check the reply values. They run when HOTKV_TEST_URL is set (for
// example hotkv://127.0.0.1:6379) and are skipped otherwise. The server
// must be empty enough that the unique key prefix is unique; the tests
// never FLUSH. Enterprise/AI subtests need a licensed server: without a
// license they are skipped (never silently passed) by requireLicensed.

var liveSeq int64

// liveOverride, when set (by the remote test), replaces HOTKV_TEST_URL as
// the target of every live test; liveKeyPrefix is the namespace they use.
var (
	liveOverride  *hotkv.Options
	liveKeyPrefix = "gosdk:"
)

func liveURL(t *testing.T) string {
	t.Helper()
	if liveOverride != nil {
		return "(remote target)"
	}
	u := os.Getenv("HOTKV_TEST_URL")
	if u == "" {
		t.Skip("HOTKV_TEST_URL not set; skipping live tests (e.g. hotkv://127.0.0.1:6379)")
	}
	return u
}

// liveOptions returns a fresh copy of the options of the live target.
func liveOptions(t *testing.T) *hotkv.Options {
	t.Helper()
	if liveOverride != nil {
		o := *liveOverride
		return &o
	}
	opts, err := hotkv.ParseURL(liveURL(t))
	if err != nil {
		t.Fatalf("invalid HOTKV_TEST_URL: %v", err)
	}
	return opts
}

// liveClient returns a client for the live target with the given protocol
// (2 or 3) and a unique key namer.
func liveClient(t *testing.T, protocol int) (*hotkv.Client, func(string) string) {
	t.Helper()
	opts := liveOptions(t)
	opts.Protocol = protocol
	opts.ReadTimeout = 10 * time.Second
	c, err := hotkv.NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("server not reachable: %v", err)
	}
	run := fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&liveSeq, 1))
	return c, func(s string) string { return liveKeyPrefix + run + ":" + s }
}

// forProtocols runs fn once on RESP2 and once on RESP3.
func forProtocols(t *testing.T, fn func(t *testing.T, c *hotkv.Client, k func(string) string)) {
	t.Helper()
	for _, p := range []int{2, 3} {
		p := p
		t.Run(fmt.Sprintf("RESP%d", p), func(t *testing.T) {
			c, k := liveClient(t, p)
			fn(t, c, k)
		})
	}
}

// requireLicensed skips (visibly) when an enterprise command answers
// NOLICENSE.
func requireLicensed(t *testing.T, err error) {
	t.Helper()
	if err != nil && hotkv.IsNoLicenseError(err) {
		t.Skipf("server has no license for this feature: %v", err)
	}
}

func ck(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		if hotkv.IsNoLicenseError(err) {
			t.Skipf("%s: server has no license for this feature: %v", what, err)
		}
		t.Fatalf("%s: unexpected error: %v", what, err)
	}
}

func eq(t *testing.T, what string, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func eqUnordered(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %q, want (any order) %q", what, got, want)
	}
}

func isNil(t *testing.T, what string, err error) {
	t.Helper()
	if err != hotkv.Nil {
		t.Errorf("%s: err = %v, want hotkv.Nil", what, err)
	}
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	d := got - want
	if d < 0 {
		d = -d
	}
	if d > tol {
		t.Errorf("%s = %v, want %v (+/- %v)", what, got, want, tol)
	}
}

var (
	posInf = math.Inf(1)
	negInf = math.Inf(-1)
)

// flat flattens a reply into its scalar leaves, in order: arrays and sets
// element by element, RESP3 maps as key, value, key, value. Integers and
// doubles are rendered in decimal. It lets a test assert on a reply
// whatever shape (flat array or nested/maps) the protocol version gives it.
func flat(vals ...hotkv.Value) []string {
	var out []string
	var walk func(v hotkv.Value)
	walk = func(v hotkv.Value) {
		switch v.Typ {
		case hotkv.RespArray, hotkv.RespSet, hotkv.RespPush:
			for _, e := range v.Elems {
				walk(e)
			}
		case hotkv.RespMap:
			for _, kv := range v.MapVal {
				walk(kv.Key)
				walk(kv.Val)
			}
		case hotkv.RespBoolean:
			if v.Bool {
				out = append(out, "1")
			} else {
				out = append(out, "0")
			}
		case hotkv.RespInteger:
			out = append(out, fmt.Sprint(v.Int))
		case hotkv.RespDouble:
			out = append(out, strconv.FormatFloat(v.Dbl, 'f', -1, 64))
		case hotkv.RespNull:
			out = append(out, "")
		default:
			out = append(out, v.Str)
		}
	}
	for _, v := range vals {
		walk(v)
	}
	return out
}

// field finds name in a flattened name/value reply and returns the value
// after it.
func field(flatVals []string, name string) (string, bool) {
	for i := 0; i+1 < len(flatVals); i++ {
		if flatVals[i] == name {
			return flatVals[i+1], true
		}
	}
	return "", false
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
