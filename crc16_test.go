// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import "testing"

func TestCRC16CheckValue(t *testing.T) {
	// The well-known CRC-16/XMODEM check value.
	if got := CRC16([]byte("123456789")); got != 0x31C3 {
		t.Fatalf("CRC16(\"123456789\") = 0x%04X, want 0x31C3", got)
	}
}

func TestKeySlot(t *testing.T) {
	cases := []struct {
		key  string
		want uint16
	}{
		{"foo", 12182},
		{"bar", 5061},
		{"hello", 866},
		{"{user1000}.following", 3443},
		{"{user1000}.followers", 3443},
		{"{}foo", 9500},
		{"foo{}{bar}", 8363},
		{"foo{{bar}}zap", 4015},
		{"foo{bar}{zap}", 5061},
	}
	for _, c := range cases {
		if got := KeySlot(c.key); got != c.want {
			t.Errorf("KeySlot(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}

func TestKeySlotHashTagMatchesPlainKey(t *testing.T) {
	if KeySlot("foo{bar}{zap}") != KeySlot("bar") {
		t.Fatal("hash tag {bar} should slot the same as the bare key \"bar\"")
	}
}

func TestKeySlotInRange(t *testing.T) {
	for _, k := range []string{"", "x", "a-very-long-key-name-indeed-for-testing-purposes"} {
		if s := KeySlot(k); s >= 16384 {
			t.Errorf("KeySlot(%q) = %d, out of range", k, s)
		}
	}
}
