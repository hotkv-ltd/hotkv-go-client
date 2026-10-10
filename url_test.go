// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import "testing"

func TestParseURLBasic(t *testing.T) {
	opts, err := ParseURL("hotkv://localhost:6399")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Addr != "localhost:6399" {
		t.Errorf("Addr = %q, want localhost:6399", opts.Addr)
	}
	if opts.TLSConfig != nil {
		t.Error("TLSConfig should be nil for hotkv://")
	}
	if opts.DB != 0 {
		t.Errorf("DB = %d, want 0", opts.DB)
	}
}

func TestParseURLDefaultPort(t *testing.T) {
	opts, err := ParseURL("hotkv://localhost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Addr != "localhost:6379" {
		t.Errorf("Addr = %q, want localhost:6379", opts.Addr)
	}
}

func TestParseURLTLSSchemes(t *testing.T) {
	for _, raw := range []string{"hotkvs://host:6400", "rediss://host:6400"} {
		opts, err := ParseURL(raw)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", raw, err)
		}
		if opts.TLSConfig == nil {
			t.Errorf("%s: expected TLSConfig to be set", raw)
		}
	}
}

func TestParseURLRedisSchemeAcceptedNoTLS(t *testing.T) {
	opts, err := ParseURL("redis://host:6379")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.TLSConfig != nil {
		t.Error("TLSConfig should be nil for redis://")
	}
}

func TestParseURLAuthAndDB(t *testing.T) {
	opts, err := ParseURL("hotkv://user:p%40ss@host.example:1234/5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Username != "user" {
		t.Errorf("Username = %q, want user", opts.Username)
	}
	if opts.Password != "p@ss" {
		t.Errorf("Password = %q, want p@ss", opts.Password)
	}
	if opts.DB != 5 {
		t.Errorf("DB = %d, want 5", opts.DB)
	}
	if opts.Addr != "host.example:1234" {
		t.Errorf("Addr = %q, want host.example:1234", opts.Addr)
	}
}

func TestParseURLPasswordOnly(t *testing.T) {
	opts, err := ParseURL("redis://:secret@host")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Username != "" {
		t.Errorf("Username = %q, want empty", opts.Username)
	}
	if opts.Password != "secret" {
		t.Errorf("Password = %q, want secret", opts.Password)
	}
}

func TestParseURLUnknownScheme(t *testing.T) {
	if _, err := ParseURL("ftp://host"); err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
}

func TestParseURLInvalidDB(t *testing.T) {
	if _, err := ParseURL("hotkv://host/not-a-number"); err == nil {
		t.Fatal("expected an error for a non-numeric database path")
	}
}
