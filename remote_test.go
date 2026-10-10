// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	hotkv "github.com/hotkv-ltd/hotkv-go-client"
)

// TestRemote connects over the internet to a TLS + password protected HotKV
// server exactly as a customer would, and runs the same value-checking live
// tests as the local suite against it. It runs only when HOTKV_REMOTE_HOST
// is set:
//
//	HOTKV_REMOTE_HOST      host or IP of the server
//	HOTKV_REMOTE_PORT      TLS port
//	HOTKV_REMOTE_PASSWORD  AUTH password (never printed, logged or stored)
//	HOTKV_REMOTE_CA        PEM file with the CA that signed the server certificate
//
// The server is shared: every key lives under a unique remote-go-<random>
// prefix and is removed afterwards, and nothing global (FLUSH*, CONFIG SET,
// ACL, SHUTDOWN, license changes) is ever issued.
func TestRemote(t *testing.T) {
	host := os.Getenv("HOTKV_REMOTE_HOST")
	if host == "" {
		t.Skip("HOTKV_REMOTE_HOST not set; skipping the remote (internet, TLS, password) tests")
	}
	port := os.Getenv("HOTKV_REMOTE_PORT")
	password := os.Getenv("HOTKV_REMOTE_PASSWORD")
	caPath := os.Getenv("HOTKV_REMOTE_CA")
	if port == "" || password == "" || caPath == "" {
		t.Fatal("HOTKV_REMOTE_PORT, HOTKV_REMOTE_PASSWORD and HOTKV_REMOTE_CA must be set together with HOTKV_REMOTE_HOST")
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read CA file: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("CA file contains no certificate")
	}
	hostPort := net.JoinHostPort(host, port)
	ctx := context.Background()

	var rnd [5]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "remote-go-" + hex.EncodeToString(rnd[:]) + ":"
	key := func(s string) string { return prefix + s }

	// The password must never reach a log line, even through an error.
	scrub := func(err error) string {
		if err == nil {
			return ""
		}
		return strings.ReplaceAll(err.Error(), password, "<password>")
	}

	// 1. hotkvs:// URL form, verified against the CA.
	u := url.URL{Scheme: "hotkvs", User: url.UserPassword("", password), Host: hostPort}
	urlOpts, err := hotkv.ParseURL(u.String())
	if err != nil {
		t.Fatalf("ParseURL: %s", scrub(err))
	}
	if urlOpts.TLSConfig == nil || urlOpts.Password != password {
		t.Fatal("hotkvs:// URL did not yield TLS and the password")
	}
	urlOpts.TLSConfig.RootCAs = roots
	urlOpts.ReadTimeout = 15 * time.Second
	urlOpts.DialTimeout = 15 * time.Second
	urlClient, err := hotkv.NewClient(urlOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer urlClient.Close()

	// 2. options form, RESP3 and RESP2.
	newOptsClient := func(protocol int) *hotkv.Client {
		c, err := hotkv.NewClient(&hotkv.Options{
			Addr:        hostPort,
			Password:    password,
			Protocol:    protocol,
			TLSConfig:   &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialTimeout: 15 * time.Second,
			ReadTimeout: 15 * time.Second,
			PoolSize:    8,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c3, c2 := newOptsClient(3), newOptsClient(2)
	defer c3.Close()
	defer c2.Close()

	t.Run("PingAndRoundTrip", func(t *testing.T) {
		for name, c := range map[string]*hotkv.Client{"URL": urlClient, "options RESP3": c3, "options RESP2": c2} {
			start := time.Now()
			pong, err := c.Ping(ctx).Result()
			first := time.Since(start) // includes the TCP + TLS + AUTH handshake
			if err != nil || pong != "PONG" {
				t.Fatalf("%s: PING = %q, %s", name, pong, scrub(err))
			}
			var samples []time.Duration
			for i := 0; i < 20; i++ {
				s := time.Now()
				if err := c.Ping(ctx).Err(); err != nil {
					t.Fatalf("%s: PING: %s", name, scrub(err))
				}
				samples = append(samples, time.Since(s))
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			p50, p90 := samples[len(samples)/2], samples[len(samples)*9/10]
			t.Logf("%-14s first command (connect+TLS+AUTH+PING) %v, warm PING p50 %v p90 %v over 20", name, first.Round(time.Millisecond), p50.Round(time.Millisecond), p90.Round(time.Millisecond))
			if p50 > 2*time.Second {
				t.Errorf("%s: warm PING p50 %v is implausibly slow", name, p50)
			}
		}
	})

	t.Run("AuthFailureIsClean", func(t *testing.T) {
		bad, err := hotkv.NewClient(&hotkv.Options{
			Addr:        hostPort,
			Password:    "definitely-wrong-" + hex.EncodeToString(rnd[:]),
			TLSConfig:   &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialTimeout: 15 * time.Second,
			ReadTimeout: 15 * time.Second,
			MaxRetries:  -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer bad.Close()
		err = bad.Ping(ctx).Err()
		if err == nil {
			t.Fatal("a wrong password was accepted")
		}
		var he *hotkv.Error
		if !errors.As(err, &he) {
			t.Fatalf("wrong password gave a non-server error: %s", scrub(err))
		}
		t.Logf("wrong password: %s", scrub(err))
		if strings.Contains(err.Error(), password) {
			t.Error("the error text contains the real password")
		}
		// no password at all: the server answers NOAUTH to the first command
		anon, _ := hotkv.NewClient(&hotkv.Options{
			Addr:        hostPort,
			TLSConfig:   &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialTimeout: 15 * time.Second,
			ReadTimeout: 15 * time.Second,
			MaxRetries:  -1,
		})
		defer anon.Close()
		err = anon.Set(ctx, key("anon"), "x", 0).Err()
		if err == nil || !(hotkv.IsNoAuthError(err) || errors.As(err, &he)) {
			t.Errorf("unauthenticated SET: %v, want a NOAUTH-style server error", scrub(err))
		}
	})

	t.Run("TLSVerification", func(t *testing.T) {
		// system roots do not know the server's CA: the handshake must fail
		strict, _ := hotkv.NewClient(&hotkv.Options{
			Addr:        hostPort,
			Password:    password,
			TLSConfig:   &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
			DialTimeout: 15 * time.Second,
			MaxRetries:  -1,
		})
		defer strict.Close()
		err := strict.Ping(ctx).Err()
		if err == nil {
			t.Fatal("a certificate that the system roots do not trust was accepted")
		}
		t.Logf("unknown CA: %s", scrub(err))
		// a name that is not in the certificate must be refused too
		wrongName, _ := hotkv.NewClient(&hotkv.Options{
			Addr:        hostPort,
			Password:    password,
			TLSConfig:   &tls.Config{ServerName: "not-the-server.example", RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialTimeout: 15 * time.Second,
			MaxRetries:  -1,
		})
		defer wrongName.Close()
		if err := wrongName.Ping(ctx).Err(); err == nil {
			t.Fatal("a certificate for another name was accepted")
		}
		// the explicit opt-out works (one extra test, per the documented option)
		insecure, _ := hotkv.NewClient(&hotkv.Options{
			Addr:        hostPort,
			Password:    password,
			TLSConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // deliberate: tests the opt-out
			DialTimeout: 15 * time.Second,
			ReadTimeout: 15 * time.Second,
		})
		defer insecure.Close()
		if err := insecure.Ping(ctx).Err(); err != nil {
			t.Errorf("InsecureSkipVerify client: %s", scrub(err))
		}
	})

	// From here on the shared live suite targets the remote server.
	liveOverride = urlOpts
	liveKeyPrefix = prefix
	t.Cleanup(func() { liveOverride = nil; liveKeyPrefix = "gosdk:" })

	t.Run("Reconnect", func(t *testing.T) {
		// kill our own connection from a second client; the next command on the
		// first one must transparently reconnect
		o := *urlOpts
		o.PoolSize = 1
		victim, _ := hotkv.NewClient(&o)
		defer victim.Close()
		id, err := victim.ClientID(ctx).Result()
		if err != nil {
			t.Fatalf("CLIENT ID: %s", scrub(err))
		}
		killer, _ := hotkv.NewClient(urlOpts)
		defer killer.Close()
		n, err := killer.Do(ctx, "CLIENT", "KILL", "ID", id).Int64()
		if err != nil || n != 1 {
			t.Skipf("cannot kill our own connection on this server (CLIENT KILL ID %d = %d, %v)", id, n, scrub(err))
		}
		time.Sleep(200 * time.Millisecond)
		if err := victim.Set(ctx, key("after-kill"), "ok", time.Minute).Err(); err != nil {
			t.Fatalf("command after the server closed the connection: %s", scrub(err))
		}
		v, _ := victim.Get(ctx, key("after-kill")).Result()
		eq(t, "value after reconnect", v, "ok")
		victim.Del(ctx, key("after-kill"))
	})

	t.Run("Concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan string, 16)
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					kk := key("conc:" + string(rune('a'+g)) + string(rune('A'+i)))
					if err := c3.Set(ctx, kk, kk, time.Minute).Err(); err != nil {
						errs <- scrub(err)
						return
					}
					if v, err := c3.Get(ctx, kk).Result(); err != nil || v != kk {
						errs <- "GET " + kk + " = " + v + " " + scrub(err)
						return
					}
					c3.Del(ctx, kk)
				}
			}(g)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Error(e)
		}
	})

	t.Run("BigValue", func(t *testing.T) {
		big := strings.Repeat("0123456789abcdef", 64*1024)
		start := time.Now()
		if err := c3.Set(ctx, key("big"), big, time.Minute).Err(); err != nil {
			t.Fatalf("SET 1 MiB: %s", scrub(err))
		}
		v, err := c3.Get(ctx, key("big")).Result()
		if err != nil || v != big {
			t.Fatalf("1 MiB round trip failed: len %d, %s", len(v), scrub(err))
		}
		t.Logf("1 MiB SET + GET over the internet: %v", time.Since(start).Round(time.Millisecond))
		c3.Del(ctx, key("big"))
	})

	// The shared suite: same checks as against the local server, RESP2 and RESP3.
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{"Strings", TestLiveStrings},
		{"Keys", TestLiveKeys},
		{"Lists", TestLiveLists},
		{"Sets", TestLiveSets},
		{"Hashes", TestLiveHashes},
		{"SortedSets", TestLiveSortedSets},
		{"Streams", TestLiveStreams},
		{"BitmapsAndHLL", TestLiveBitmapsAndHLL},
		{"PubSub", TestLivePubSub},
		{"PubSubChannelAndClose", TestLivePubSubChannelAndClose},
		{"PipelineAndTransactions", TestLivePipelineAndTransactions},
		{"ConcurrentUse", TestLiveConcurrentUse},
		{"DoGenericCommand", TestLiveDoGenericCommand},
		{"DoProbabilistic", TestLiveDoProbabilistic},
		{"DoNewerCommands", TestLiveDoNewerHashAndStringCommands},
		{"DoGeoListScript", TestLiveDoGeoListScript},
		{"Tags", TestLiveTagsAndNanoTTL},
		{"VSIM", TestLiveVSIM},
		{"VSIMPersistence", TestLiveVSIMPersistence},
		{"GCRA", TestLiveGCRA},
		{"SemanticCache", TestLiveSemanticCache},
		{"PromptCache", TestLivePromptCache},
		{"KeyedCaches", TestLiveKeyedCaches},
		{"LLMStats", TestLiveLLMStats},
		{"LLMBudgetFloats", TestLiveLLMBudgetFloats},
		{"LLMBudget", TestLiveLLMBudget},
		{"PromptRegistry", TestLivePromptRegistry},
		{"EmbedDedup", TestLiveEmbedDedup},
		{"AgentMemory", TestLiveAgentMemory},
		{"RAG", TestLiveRAG},
		{"RateLimit", TestLiveRateLimit},
		{"TimeSeries", TestLiveTimeSeries},
		{"FeatureStore", TestLiveFeatureStore},
	} {
		t.Run(tc.name, tc.fn)
	}

	// nothing of ours may be left behind
	t.Run("NoLeftovers", func(t *testing.T) {
		left, err := c3.Keys(ctx, prefix+"*").Result()
		if err != nil {
			t.Fatalf("KEYS: %s", scrub(err))
		}
		for i := 0; i < len(left); i += 200 {
			end := i + 200
			if end > len(left) {
				end = len(left)
			}
			c3.Del(ctx, left[i:end]...)
		}
		if len(left) > 0 {
			t.Logf("removed %d leftover keys under %s", len(left), prefix)
		}
	})
}
