// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// --- Reader tests ---

func TestReadSimpleString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ok", "+OK\r\n", "OK"},
		{"pong", "+PONG\r\n", "PONG"},
		{"empty", "+\r\n", ""},
		{"spaces", "+hello world\r\n", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Typ != RespSimpleString {
				t.Fatalf("type = %v, want SimpleString", v.Typ)
			}
			if v.Str != tt.want {
				t.Fatalf("Str = %q, want %q", v.Str, tt.want)
			}
		})
	}
}

func TestReadError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"generic", "-ERR unknown command\r\n", "ERR unknown command"},
		{"wrongtype", "-WRONGTYPE Operation against a key holding the wrong kind of value\r\n",
			"WRONGTYPE Operation against a key holding the wrong kind of value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Typ != RespError {
				t.Fatalf("type = %v, want Error", v.Typ)
			}
			if v.Str != tt.want {
				t.Fatalf("Str = %q, want %q", v.Str, tt.want)
			}
			if !v.IsErr() {
				t.Fatal("IsErr() should be true")
			}
		})
	}
}

func TestReadInteger(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{"zero", ":0\r\n", 0},
		{"positive", ":1000\r\n", 1000},
		{"negative", ":-42\r\n", -42},
		{"large", ":9223372036854775807\r\n", 9223372036854775807},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Typ != RespInteger {
				t.Fatalf("type = %v, want Integer", v.Typ)
			}
			if v.Int != tt.want {
				t.Fatalf("Int = %d, want %d", v.Int, tt.want)
			}
		})
	}
}

func TestReadBulkString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		isNil bool
	}{
		{"hello", "$5\r\nhello\r\n", "hello", false},
		{"empty", "$0\r\n\r\n", "", false},
		{"null", "$-1\r\n", "", true},
		{"binary", "$4\r\n\x00\x01\x02\x03\r\n", "\x00\x01\x02\x03", false},
		{"with_crlf", "$7\r\nhel\r\nlo\r\n", "hel\r\nlo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.isNil {
				if !v.IsNil() {
					t.Fatal("expected nil value")
				}
				return
			}
			if v.Typ != RespBulkString {
				t.Fatalf("type = %v, want BulkString", v.Typ)
			}
			if v.Str != tt.want {
				t.Fatalf("Str = %q, want %q", v.Str, tt.want)
			}
		})
	}
}

func TestReadArray(t *testing.T) {
	tests := []struct {
		name  string
		input string
		count int
		isNil bool
	}{
		{"null", "*-1\r\n", 0, true},
		{"empty", "*0\r\n", 0, false},
		{"two_bulks", "*2\r\n$3\r\nfoo\r\n$3\r\nbar\r\n", 2, false},
		{"mixed", "*3\r\n:1\r\n$5\r\nhello\r\n+OK\r\n", 3, false},
		{"nested", "*2\r\n*2\r\n:1\r\n:2\r\n*2\r\n:3\r\n:4\r\n", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.isNil {
				if !v.IsNil() {
					t.Fatal("expected nil value")
				}
				return
			}
			if v.Typ != RespArray {
				t.Fatalf("type = %v, want Array", v.Typ)
			}
			if len(v.Elems) != tt.count {
				t.Fatalf("len(Elems) = %d, want %d", len(v.Elems), tt.count)
			}
		})
	}
}

func TestReadArrayContent(t *testing.T) {
	input := "*3\r\n:1\r\n$5\r\nhello\r\n+OK\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if v.Elems[0].Typ != RespInteger || v.Elems[0].Int != 1 {
		t.Errorf("elem[0] = %+v, want Integer(1)", v.Elems[0])
	}
	if v.Elems[1].Typ != RespBulkString || v.Elems[1].Str != "hello" {
		t.Errorf("elem[1] = %+v, want BulkString(hello)", v.Elems[1])
	}
	if v.Elems[2].Typ != RespSimpleString || v.Elems[2].Str != "OK" {
		t.Errorf("elem[2] = %+v, want SimpleString(OK)", v.Elems[2])
	}
}

func TestReadNestedArrayContent(t *testing.T) {
	input := "*2\r\n*2\r\n:1\r\n:2\r\n*2\r\n:3\r\n:4\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inner0 := v.Elems[0]
	if inner0.Typ != RespArray || len(inner0.Elems) != 2 {
		t.Fatalf("elem[0] wrong: %+v", inner0)
	}
	if inner0.Elems[0].Int != 1 || inner0.Elems[1].Int != 2 {
		t.Fatalf("elem[0] values wrong")
	}

	inner1 := v.Elems[1]
	if inner1.Typ != RespArray || len(inner1.Elems) != 2 {
		t.Fatalf("elem[1] wrong: %+v", inner1)
	}
	if inner1.Elems[0].Int != 3 || inner1.Elems[1].Int != 4 {
		t.Fatalf("elem[1] values wrong")
	}
}

// --- RESP3 type tests ---

func TestReadNull(t *testing.T) {
	r := NewRespReader(strings.NewReader("_\r\n"))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.IsNil() {
		t.Fatal("expected nil")
	}
	if v.Typ != RespNull {
		t.Fatalf("type = %v, want Null", v.Typ)
	}
}

func TestReadDouble(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  float64
		isNaN bool
	}{
		{"positive", ",3.14\r\n", 3.14, false},
		{"negative", ",-1.5\r\n", -1.5, false},
		{"zero", ",0\r\n", 0, false},
		{"inf", ",inf\r\n", math.Inf(1), false},
		{"neginf", ",-inf\r\n", math.Inf(-1), false},
		{"nan", ",nan\r\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Typ != RespDouble {
				t.Fatalf("type = %v, want Double", v.Typ)
			}
			if tt.isNaN {
				if !math.IsNaN(v.Dbl) {
					t.Fatalf("expected NaN, got %f", v.Dbl)
				}
			} else if v.Dbl != tt.want {
				t.Fatalf("Dbl = %f, want %f", v.Dbl, tt.want)
			}
		})
	}
}

func TestReadBoolean(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"#t\r\n", true},
		{"#f\r\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			r := NewRespReader(strings.NewReader(tt.input))
			v, err := r.ReadValue()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Typ != RespBoolean {
				t.Fatalf("type = %v, want Boolean", v.Typ)
			}
			if v.Bool != tt.want {
				t.Fatalf("Bool = %v, want %v", v.Bool, tt.want)
			}
		})
	}
}

func TestReadBlobError(t *testing.T) {
	input := "!11\r\nERR unknown\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespBlobError {
		t.Fatalf("type = %v, want BlobError", v.Typ)
	}
	if v.Str != "ERR unknown" {
		t.Fatalf("Str = %q, want %q", v.Str, "ERR unknown")
	}
	if !v.IsErr() {
		t.Fatal("IsErr() should be true for blob error")
	}
}

func TestReadVerbatimString(t *testing.T) {
	input := "=15\r\ntxt:Some string\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespVerbatimStr {
		t.Fatalf("type = %v, want VerbatimStr", v.Typ)
	}
	if v.Str != "txt:Some string" {
		t.Fatalf("Str = %q, want %q", v.Str, "txt:Some string")
	}
	if v.VerbatimEncoding() != "txt" {
		t.Fatalf("VerbatimEncoding() = %q, want %q", v.VerbatimEncoding(), "txt")
	}
	if v.VerbatimContent() != "Some string" {
		t.Fatalf("VerbatimContent() = %q, want %q", v.VerbatimContent(), "Some string")
	}
}

func TestReadBigNumber(t *testing.T) {
	input := "(3492890328409238509324850943850943825024385\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespBigNumber {
		t.Fatalf("type = %v, want BigNumber", v.Typ)
	}
	bi := v.BigInt()
	if bi == nil {
		t.Fatal("BigInt() returned nil")
	}
	if bi.String() != "3492890328409238509324850943850943825024385" {
		t.Fatalf("BigInt() = %s", bi.String())
	}
}

func TestReadMap(t *testing.T) {
	// %2\r\n +key1\r\n :1\r\n +key2\r\n :2\r\n
	input := "%2\r\n+key1\r\n:1\r\n+key2\r\n:2\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespMap {
		t.Fatalf("type = %v, want Map", v.Typ)
	}
	if len(v.MapVal) != 2 {
		t.Fatalf("len(MapVal) = %d, want 2", len(v.MapVal))
	}
	if v.MapVal[0].Key.Str != "key1" || v.MapVal[0].Val.Int != 1 {
		t.Fatalf("pair[0] = %+v", v.MapVal[0])
	}
	if v.MapVal[1].Key.Str != "key2" || v.MapVal[1].Val.Int != 2 {
		t.Fatalf("pair[1] = %+v", v.MapVal[1])
	}
}

func TestReadSet(t *testing.T) {
	input := "~3\r\n+a\r\n+b\r\n+c\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespSet {
		t.Fatalf("type = %v, want Set", v.Typ)
	}
	if len(v.Elems) != 3 {
		t.Fatalf("len(Elems) = %d, want 3", len(v.Elems))
	}
}

func TestReadPush(t *testing.T) {
	// >3\r\n $7\r\nmessage\r\n $5\r\nhello\r\n $5\r\nworld\r\n
	input := ">3\r\n$7\r\nmessage\r\n$5\r\nhello\r\n$5\r\nworld\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespPush {
		t.Fatalf("type = %v, want Push", v.Typ)
	}
	if len(v.Elems) != 3 {
		t.Fatalf("len(Elems) = %d, want 3", len(v.Elems))
	}
	if v.Elems[0].Str != "message" {
		t.Fatalf("elem[0] = %q, want %q", v.Elems[0].Str, "message")
	}
}

func TestReadAttribute(t *testing.T) {
	// Attribute with 1 pair, followed by a simple string value.
	input := "|1\r\n+ttl\r\n:3600\r\n+OK\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The actual value should be the simple string.
	if v.Typ != RespSimpleString || v.Str != "OK" {
		t.Fatalf("value = %+v, want SimpleString(OK)", v)
	}
	// Attributes should be attached.
	if v.Attrs == nil {
		t.Fatal("expected attributes, got nil")
	}
	if v.Attrs.Typ != RespAttribute {
		t.Fatalf("attrs type = %v, want Attribute", v.Attrs.Typ)
	}
	if len(v.Attrs.MapVal) != 1 {
		t.Fatalf("attrs pair count = %d, want 1", len(v.Attrs.MapVal))
	}
	if v.Attrs.MapVal[0].Key.Str != "ttl" || v.Attrs.MapVal[0].Val.Int != 3600 {
		t.Fatalf("attrs pair = %+v", v.Attrs.MapVal[0])
	}
}

func TestReadInline(t *testing.T) {
	input := "PING\r\n"
	r := NewRespReader(strings.NewReader(input))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Typ != RespArray {
		t.Fatalf("type = %v, want Array", v.Typ)
	}
	if len(v.Elems) != 1 || v.Elems[0].Str != "PING" {
		t.Fatalf("inline parse: %+v", v)
	}
}

func TestReadMultipleValues(t *testing.T) {
	input := "+OK\r\n:42\r\n$5\r\nhello\r\n"
	r := NewRespReader(strings.NewReader(input))

	v1, err := r.ReadValue()
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	if v1.Typ != RespSimpleString || v1.Str != "OK" {
		t.Fatalf("v1 = %+v", v1)
	}

	v2, err := r.ReadValue()
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v2.Typ != RespInteger || v2.Int != 42 {
		t.Fatalf("v2 = %+v", v2)
	}

	v3, err := r.ReadValue()
	if err != nil {
		t.Fatalf("v3: %v", err)
	}
	if v3.Typ != RespBulkString || v3.Str != "hello" {
		t.Fatalf("v3 = %+v", v3)
	}
}

// --- Error cases ---

func TestReadInvalidInteger(t *testing.T) {
	r := NewRespReader(strings.NewReader(":abc\r\n"))
	_, err := r.ReadValue()
	if err == nil {
		t.Fatal("expected error for invalid integer")
	}
}

func TestReadInvalidBoolean(t *testing.T) {
	r := NewRespReader(strings.NewReader("#x\r\n"))
	_, err := r.ReadValue()
	if err == nil {
		t.Fatal("expected error for invalid boolean")
	}
}

func TestReadBulkStringOverflow(t *testing.T) {
	// Length exceeding max.
	r := NewRespReader(strings.NewReader("$999999999999\r\n"))
	_, err := r.ReadValue()
	if err == nil {
		t.Fatal("expected error for oversized bulk string")
	}
}

func TestReadArrayOverflow(t *testing.T) {
	r := NewRespReader(strings.NewReader("*999999999999\r\n"))
	_, err := r.ReadValue()
	if err == nil {
		t.Fatal("expected error for oversized array")
	}
}

func TestReadNestingDepthExceeded(t *testing.T) {
	// Build a deeply nested array: *1\r\n*1\r\n*1\r\n ... past maxNestingDepth.
	var sb strings.Builder
	for i := 0; i <= maxNestingDepth+5; i++ {
		sb.WriteString("*1\r\n")
	}
	sb.WriteString(":1\r\n")
	r := NewRespReader(strings.NewReader(sb.String()))
	_, err := r.ReadValue()
	if err == nil {
		t.Fatal("expected nesting depth error")
	}
}

// --- Writer tests ---

func TestWriteCommand(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteCommand("SET", "key", "value"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*3\r\n$3\r\nSET\r\n$3\r\nkey\r\n$5\r\nvalue\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteCommandGet(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteCommand("GET", "mykey"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*2\r\n$3\r\nGET\r\n$5\r\nmykey\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteCommandPing(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteCommand("PING"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*1\r\n$4\r\nPING\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteCommandBytes(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteCommandBytes([]byte("SET"), []byte("k"), []byte("\x00\x01")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$2\r\n\x00\x01\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteArgs(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	err := w.WriteArgs([]interface{}{
		"SET", "counter", int64(42), true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*4\r\n$3\r\nSET\r\n$7\r\ncounter\r\n$2\r\n42\r\n$1\r\n1\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteArgsFalse(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	err := w.WriteArgs([]interface{}{false})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*1\r\n$1\r\n0\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteArgsNil(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	err := w.WriteArgs([]interface{}{nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*1\r\n$-1\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteArgsUnsupported(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	err := w.WriteArgs([]interface{}{struct{}{}})
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
}

func TestWriteSimpleString(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteSimpleString("OK"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "+OK\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteErrorMsg(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteError("ERR something failed"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "-ERR something failed\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteInteger(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteInteger(-42); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != ":-42\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteNull(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteNull(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "_\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWritePipelinedCommands(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteCommand("SET", "a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCommand("SET", "b", "2"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCommand("GET", "a"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n" +
		"*3\r\n$3\r\nSET\r\n$1\r\nb\r\n$1\r\n2\r\n" +
		"*2\r\n$3\r\nGET\r\n$1\r\na\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("pipeline mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// --- Roundtrip test ---

func TestRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	if err := w.WriteCommand("MGET", "key1", "key2", "key3"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	r := NewRespReader(&buf)
	v, err := r.ReadValue()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if v.Typ != RespArray || len(v.Elems) != 4 {
		t.Fatalf("roundtrip: %+v", v)
	}
	if v.Elems[0].Str != "MGET" {
		t.Fatalf("elem[0] = %q", v.Elems[0].Str)
	}
	if v.Elems[3].Str != "key3" {
		t.Fatalf("elem[3] = %q", v.Elems[3].Str)
	}
}

func TestWriteArgsFloat(t *testing.T) {
	var buf bytes.Buffer
	w := NewRespWriter(&buf)
	err := w.WriteArgs([]interface{}{"INCRBYFLOAT", "key", float64(1.5)})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "*3\r\n$11\r\nINCRBYFLOAT\r\n$3\r\nkey\r\n$3\r\n1.5\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// --- Benchmarks ---

func BenchmarkReadBulkString(b *testing.B) {
	data := []byte("$11\r\nhello world\r\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewRespReader(bytes.NewReader(data))
		if _, err := r.ReadValue(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadArray(b *testing.B) {
	data := []byte("*3\r\n$3\r\nSET\r\n$3\r\nkey\r\n$5\r\nvalue\r\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewRespReader(bytes.NewReader(data))
		if _, err := r.ReadValue(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadInteger(b *testing.B) {
	data := []byte(":123456789\r\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewRespReader(bytes.NewReader(data))
		if _, err := r.ReadValue(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteCommand(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		w := NewRespWriter(&buf)
		_ = w.WriteCommand("SET", "mykey", "myvalue")
		_ = w.Flush()
	}
}

func BenchmarkWritePipeline(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		w := NewRespWriter(&buf)
		for j := 0; j < 100; j++ {
			_ = w.WriteCommand("SET", "key", "value")
		}
		_ = w.Flush()
	}
}
