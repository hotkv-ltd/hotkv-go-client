// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import "testing"

func TestBuildPromptCacheKeyGoldenVector(t *testing.T) {
	// This exact input/output pair is the cross-SDK key vector: every
	// official HotKV SDK must derive the same key for it.
	in := PromptCacheKeyInput{
		Provider: "openai",
		Model:    "gpt-4",
		System:   "You are helpful.",
		Messages: []PromptMessage{{Role: "user", Content: "Hello"}},
		Params:   map[string]string{"temperature": "0.7", "top_p": "1"},
	}
	const want = "14caa0bbab1edd17eb7205f946a358d94a5e47db203e5514f6e1c405d987311b"
	if got := BuildPromptCacheKey(in); got != want {
		t.Fatalf("BuildPromptCacheKey golden vector mismatch:\n got  %s\n want %s", got, want)
	}
}

func TestBuildPromptCacheKeyDeterministic(t *testing.T) {
	in := PromptCacheKeyInput{
		Provider: "anthropic",
		Model:    "claude",
		Messages: []PromptMessage{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "2+2?"},
		},
		Params: map[string]string{"max_tokens": "100", "temperature": "0.0"},
	}
	first := BuildPromptCacheKey(in)
	for i := 0; i < 20; i++ {
		if got := BuildPromptCacheKey(in); got != first {
			t.Fatalf("BuildPromptCacheKey is not deterministic across repeated calls: %s != %s", got, first)
		}
	}
	if len(first) != 64 {
		t.Errorf("key length = %d, want 64 (hex SHA-256)", len(first))
	}
}

func TestBuildPromptCacheKeyParamOrderIndependent(t *testing.T) {
	base := PromptCacheKeyInput{Provider: "p", Model: "m"}

	a := base
	a.Params = map[string]string{"a": "1", "b": "2", "c": "3"}
	b := base
	b.Params = map[string]string{"c": "3", "a": "1", "b": "2"}

	if BuildPromptCacheKey(a) != BuildPromptCacheKey(b) {
		t.Fatal("keys must not depend on map insertion/iteration order")
	}
}

func TestBuildPromptCacheKeyDiffersOnContentChange(t *testing.T) {
	a := PromptCacheKeyInput{Provider: "p", Model: "m", Messages: []PromptMessage{{Role: "user", Content: "hi"}}}
	b := PromptCacheKeyInput{Provider: "p", Model: "m", Messages: []PromptMessage{{Role: "user", Content: "hi!"}}}
	if BuildPromptCacheKey(a) == BuildPromptCacheKey(b) {
		t.Fatal("different message content must produce different keys")
	}
}

func TestBuildPromptCacheKeyEmptyFieldsStillDeterministic(t *testing.T) {
	in := PromptCacheKeyInput{Provider: "p", Model: "m"}
	k1 := BuildPromptCacheKey(in)
	k2 := BuildPromptCacheKey(PromptCacheKeyInput{Provider: "p", Model: "m", Messages: nil, Params: nil})
	if k1 != k2 {
		t.Fatal("nil vs zero-value Messages/Params should hash identically")
	}
}

func TestBuildPromptCacheKeyEscaping(t *testing.T) {
	in := PromptCacheKeyInput{
		Provider: "p",
		Model:    "m",
		System:   "line1\nline2\t\"quoted\"\\backslash",
	}
	// Must not panic and must be a valid 64-char hex string.
	got := BuildPromptCacheKey(in)
	if len(got) != 64 {
		t.Fatalf("key length = %d, want 64", len(got))
	}
}
