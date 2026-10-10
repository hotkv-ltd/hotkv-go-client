// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cmd is the base of every command result. It stores the raw RESP value and error.
type Cmd struct {
	val Value
	err error
}

// SetVal sets the raw RESP value on the command result.
func (c *Cmd) SetVal(v Value) { c.val = v }

// SetErr sets the error on the command result.
func (c *Cmd) SetErr(err error) { c.err = err }

// Err returns the command error, if any.
func (c *Cmd) Err() error { return c.err }

// Val returns the raw RESP value.
func (c *Cmd) Val() Value { return c.val }

// Result returns the reply converted to plain Go values (string for simple,
// bulk and verbatim strings, int64, float64, bool, []interface{} for arrays
// and sets, map[interface{}]interface{} for maps, nil for a null reply) and
// the command error. A null reply is reported as (nil, Nil).
func (c *Cmd) Result() (interface{}, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, Nil
	}
	return valueToInterface(c.val), nil
}

// Text returns a string reply. Integers and doubles are rendered in
// decimal; a null reply is ("", Nil).
func (c *Cmd) Text() (string, error) {
	if c.err != nil {
		return "", c.err
	}
	switch c.val.Typ {
	case RespNull:
		return "", Nil
	case RespInteger:
		return strconv.FormatInt(c.val.Int, 10), nil
	case RespDouble:
		return strconv.FormatFloat(c.val.Dbl, 'f', -1, 64), nil
	case RespBoolean:
		if c.val.Bool {
			return "1", nil
		}
		return "0", nil
	case RespVerbatimStr:
		return c.val.VerbatimContent(), nil
	case RespArray, RespSet, RespPush, RespMap, RespAttribute:
		return "", fmt.Errorf("hotkv: reply is %s, not a string", c.val.typeName())
	}
	return c.val.Str, nil
}

// Int64 returns an integer reply (or a string reply holding an integer).
func (c *Cmd) Int64() (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	switch c.val.Typ {
	case RespNull:
		return 0, Nil
	case RespInteger:
		return c.val.Int, nil
	case RespBoolean:
		if c.val.Bool {
			return 1, nil
		}
		return 0, nil
	}
	s, err := c.Text()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

// Float64 returns a double reply (or a string/integer reply holding a number).
func (c *Cmd) Float64() (float64, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.val.Typ == RespDouble {
		return c.val.Dbl, nil
	}
	s, err := c.Text()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

// Bool returns a boolean reply, or an integer 0/1, or "OK"/"1".
func (c *Cmd) Bool() (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	switch c.val.Typ {
	case RespNull:
		return false, Nil
	case RespBoolean:
		return c.val.Bool, nil
	case RespInteger:
		return c.val.Int != 0, nil
	}
	s, err := c.Text()
	if err != nil {
		return false, err
	}
	return s == "1" || strings.EqualFold(s, "OK") || strings.EqualFold(s, "true"), nil
}

// Slice returns an array/set/push reply as []interface{} (elements
// converted as in Result).
func (c *Cmd) Slice() ([]interface{}, error) {
	if c.err != nil {
		return nil, c.err
	}
	switch c.val.Typ {
	case RespNull:
		return nil, Nil
	case RespArray, RespSet, RespPush:
		out := make([]interface{}, len(c.val.Elems))
		for i, e := range c.val.Elems {
			out[i] = valueToInterface(e)
		}
		return out, nil
	}
	return nil, fmt.Errorf("hotkv: reply is %s, not an array", c.val.typeName())
}

// StringSlice returns an array reply whose elements are all strings or
// integers, rendered as strings.
func (c *Cmd) StringSlice() ([]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	switch c.val.Typ {
	case RespNull:
		return nil, Nil
	case RespArray, RespSet, RespPush:
		out := make([]string, len(c.val.Elems))
		for i, e := range c.val.Elems {
			sub := Cmd{val: e}
			s, err := sub.Text()
			if err != nil && err != Nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("hotkv: reply is %s, not an array", c.val.typeName())
}

// valueToInterface converts a Value to plain Go types for Cmd.Result.
func valueToInterface(v Value) interface{} {
	switch v.Typ {
	case RespNull:
		return nil
	case RespInteger:
		return v.Int
	case RespDouble:
		return v.Dbl
	case RespBoolean:
		return v.Bool
	case RespVerbatimStr:
		return v.VerbatimContent()
	case RespError, RespBlobError:
		return NewError(v.Str)
	case RespArray, RespSet, RespPush:
		out := make([]interface{}, len(v.Elems))
		for i, e := range v.Elems {
			out[i] = valueToInterface(e)
		}
		return out
	case RespMap:
		out := make(map[interface{}]interface{}, len(v.MapVal))
		for _, kv := range v.MapVal {
			k := valueToInterface(kv.Key)
			switch k.(type) {
			case string, int64, float64, bool, nil:
			default:
				k = fmt.Sprint(k)
			}
			out[k] = valueToInterface(kv.Val)
		}
		return out
	}
	return v.Str
}

// typeName names a reply type for error messages.
func (v Value) typeName() string {
	switch v.Typ {
	case RespSimpleString:
		return "a simple string"
	case RespError, RespBlobError:
		return "an error"
	case RespInteger:
		return "an integer"
	case RespBulkString:
		return "a bulk string"
	case RespArray:
		return "an array"
	case RespNull:
		return "null"
	case RespDouble:
		return "a double"
	case RespBoolean:
		return "a boolean"
	case RespVerbatimStr:
		return "a verbatim string"
	case RespBigNumber:
		return "a big number"
	case RespMap:
		return "a map"
	case RespSet:
		return "a set"
	case RespAttribute:
		return "an attribute"
	case RespPush:
		return "a push"
	}
	return "an unknown type"
}

// --- StatusCmd: for commands that return +OK ---

// StatusCmd holds the result of a command that returns a simple string status.
type StatusCmd struct {
	Cmd
}

// NewStatusCmd creates a StatusCmd with the given error.
func NewStatusCmd(err error) *StatusCmd {
	return &StatusCmd{Cmd: Cmd{err: err}}
}

// Result returns the status string and error.
func (c *StatusCmd) Result() (string, error) {
	return c.val.Str, c.err
}

// --- StringCmd: for commands that return bulk strings ---

// StringCmd holds the result of a command that returns a string.
type StringCmd struct {
	Cmd
}

// NewStringCmd creates a StringCmd with the given error.
func NewStringCmd(err error) *StringCmd {
	return &StringCmd{Cmd: Cmd{err: err}}
}

// Result returns the string value and error.
func (c *StringCmd) Result() (string, error) {
	if c.err != nil {
		return "", c.err
	}
	if c.val.IsNil() {
		return "", Nil
	}
	// RESP3 replies some commands (INFO, CLIENT INFO) as a verbatim string,
	// whose raw Str carries a leading "txt:" or "mkd:" encoding prefix; strip
	// it so the result is identical to the RESP2 plain bulk string reply.
	if c.val.Typ == RespVerbatimStr {
		return c.val.VerbatimContent(), nil
	}
	return c.val.Str, nil
}

// Bytes returns the value as a byte slice.
func (c *StringCmd) Bytes() ([]byte, error) {
	s, err := c.Result()
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// Int64 parses the string as an int64.
func (c *StringCmd) Int64() (int64, error) {
	s, err := c.Result()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

// Float64 parses the string as a float64.
func (c *StringCmd) Float64() (float64, error) {
	s, err := c.Result()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

// --- IntCmd: for commands that return integers ---

// IntCmd holds the result of a command that returns an integer.
type IntCmd struct {
	Cmd
}

// NewIntCmd creates an IntCmd with the given error.
func NewIntCmd(err error) *IntCmd {
	return &IntCmd{Cmd: Cmd{err: err}}
}

// Result returns the integer value and error.
//
// A null reply (for example ZRANK of a member that is not in the set, or
// MEMORY USAGE of a missing key) is reported as (0, Nil) so it cannot be
// mistaken for the integer 0.
func (c *IntCmd) Result() (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.val.IsNil() {
		return 0, Nil
	}
	return c.val.Int, nil
}

// --- BoolCmd: for commands that return 0/1 ---

// BoolCmd holds the result of a command that returns a boolean via an integer reply.
type BoolCmd struct {
	Cmd
}

// NewBoolCmd creates a BoolCmd with the given error.
func NewBoolCmd(err error) *BoolCmd {
	return &BoolCmd{Cmd: Cmd{err: err}}
}

// Result returns the boolean value and error.
func (c *BoolCmd) Result() (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	switch c.val.Typ {
	case RespBoolean:
		return c.val.Bool, nil
	case RespNull:
		// SET ... NX / XX answer a null reply when the condition failed.
		return false, nil
	case RespSimpleString, RespBulkString:
		// SET ... NX / XX answer +OK when the write happened.
		return c.val.Str == "OK" || c.val.Str == "1", nil
	}
	return c.val.Int != 0, nil
}

// --- FloatCmd: for commands that return floats ---

// FloatCmd holds the result of a command that returns a float.
type FloatCmd struct {
	Cmd
}

// NewFloatCmd creates a FloatCmd with the given error.
func NewFloatCmd(err error) *FloatCmd {
	return &FloatCmd{Cmd: Cmd{err: err}}
}

// Result returns the float value and error.
func (c *FloatCmd) Result() (float64, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.val.IsNil() {
		return 0, Nil
	}
	return valueFloat(c.val)
}

// valueFloat decodes a numeric reply, whether sent as a RESP3 double
// (ZSCORE, ZINCRBY, INCRBYFLOAT and similar under HELLO 3) or as a RESP2
// bulk string holding the formatted number (including "inf"/"-inf"/"nan").
func valueFloat(v Value) (float64, error) {
	if v.Typ == RespDouble {
		return v.Dbl, nil
	}
	return strconv.ParseFloat(v.Str, 64)
}

// --- DurationCmd: for TTL commands ---

// DurationCmd holds the result of a command that returns a duration.
type DurationCmd struct {
	Cmd
	precision time.Duration
}

// NewDurationCmd creates a DurationCmd with the given precision and error.
func NewDurationCmd(precision time.Duration, err error) *DurationCmd {
	return &DurationCmd{precision: precision, Cmd: Cmd{err: err}}
}

// Result returns the duration value and error.
// Returns -1 for no expiry, -2 for key not found (standard RESP convention).
func (c *DurationCmd) Result() (time.Duration, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.val.Int < 0 {
		return time.Duration(c.val.Int), nil
	}
	return time.Duration(c.val.Int) * c.precision, nil
}

// --- StringSliceCmd: for commands that return arrays of strings ---

// StringSliceCmd holds the result of a command that returns a string slice.
type StringSliceCmd struct {
	Cmd
}

// NewStringSliceCmd creates a StringSliceCmd with the given error.
func NewStringSliceCmd(err error) *StringSliceCmd {
	return &StringSliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the string slice and error.
func (c *StringSliceCmd) Result() ([]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, Nil
	}
	result := make([]string, len(c.val.Elems))
	for i, e := range c.val.Elems {
		result[i] = scalarString(e)
	}
	return result, nil
}

// --- MapStringStringCmd: for HGETALL ---

// MapStringStringCmd holds the result of a command that returns a map of strings.
type MapStringStringCmd struct {
	Cmd
}

// NewMapStringStringCmd creates a MapStringStringCmd with the given error.
func NewMapStringStringCmd(err error) *MapStringStringCmd {
	return &MapStringStringCmd{Cmd: Cmd{err: err}}
}

// Result returns the map and error.
func (c *MapStringStringCmd) Result() (map[string]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, Nil
	}
	pairs, err := valuePairs(c.val)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		m[scalarString(kv.Key)] = scalarString(kv.Val)
	}
	return m, nil
}

// scalarString renders a scalar reply as a string whatever its wire type:
// integers, doubles and booleans (as returned by, for example, PUBSUB
// NUMSUB, whose counts are RESP integers) in decimal, verbatim strings
// without their encoding prefix, null as "".
func scalarString(v Value) string {
	switch v.Typ {
	case RespInteger:
		return strconv.FormatInt(v.Int, 10)
	case RespDouble:
		return strconv.FormatFloat(v.Dbl, 'f', -1, 64)
	case RespBoolean:
		if v.Bool {
			return "1"
		}
		return "0"
	case RespVerbatimStr:
		return v.VerbatimContent()
	case RespNull:
		return ""
	}
	return v.Str
}

// valuePairs returns a map shaped reply, either RESP3's native map (e.g.
// HGETALL/CONFIG GET under HELLO 3) or a flat alternating array
// `[k1, v1, k2, v2, ...]` (RESP2, or RESP3 without a native map reply for
// that command), as an ordered list of key/value pairs. Callers never need
// to know which wire shape the connection's negotiated protocol produced.
func valuePairs(v Value) ([]KeyValue, error) {
	if v.Typ == RespMap {
		return v.MapVal, nil
	}
	if len(v.Elems)%2 != 0 {
		return nil, fmt.Errorf("hotkv: unexpected odd number of elements in a map reply")
	}
	pairs := make([]KeyValue, len(v.Elems)/2)
	for i := 0; i < len(v.Elems); i += 2 {
		pairs[i/2] = KeyValue{Key: v.Elems[i], Val: v.Elems[i+1]}
	}
	return pairs, nil
}

// valuePairSlice returns a "paired" reply, either RESP3's native array of
// 2-element pairs (e.g. ZRANGE ... WITHSCORES, HRANDFIELD ... WITHVALUES
// under HELLO 3) or a flat alternating array `[a1, b1, a2, b2, ...]`
// (RESP2, or RESP3 without a native paired reply for that command), as an
// ordered list of 2-element pairs.
func valuePairSlice(v Value) ([][2]Value, error) {
	elems := v.Elems
	if len(elems) > 0 && elems[0].Typ == RespArray {
		out := make([][2]Value, len(elems))
		for i, e := range elems {
			if len(e.Elems) != 2 {
				return nil, fmt.Errorf("hotkv: expected a 2 element pair, got %d elements", len(e.Elems))
			}
			out[i] = [2]Value{e.Elems[0], e.Elems[1]}
		}
		return out, nil
	}
	if len(elems)%2 != 0 {
		return nil, fmt.Errorf("hotkv: unexpected odd number of elements in a paired reply")
	}
	out := make([][2]Value, len(elems)/2)
	for i := 0; i < len(elems); i += 2 {
		out[i/2] = [2]Value{elems[i], elems[i+1]}
	}
	return out, nil
}

// namedFields parses a STATS/GET/STATUS-like reply, a RESP3 map or a flat
// name/value array, into a lookup from field name to raw Value. Every such
// reply is decoded by field name rather than position: missing fields
// default to zero/empty (see fieldStr/fieldInt/fieldFloat) and unknown extra
// fields are simply never looked up, so the SDK keeps working when the
// server adds fields.
func namedFields(v Value) (map[string]Value, error) {
	pairs, err := valuePairs(v)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Value, len(pairs))
	for _, kv := range pairs {
		m[kv.Key.Str] = kv.Val
	}
	return m, nil
}

// fieldStr returns the string form of field name, or "" when absent.
func fieldStr(m map[string]Value, name string) string {
	if v, ok := m[name]; ok {
		return scalarString(v)
	}
	return ""
}

// fieldInt returns the integer form of field name, or 0 when absent.
func fieldInt(m map[string]Value, name string) int64 {
	if v, ok := m[name]; ok {
		return valInt(v)
	}
	return 0
}

// fieldFloat returns the float form of field name, or 0 when absent or
// unparseable.
func fieldFloat(m map[string]Value, name string) float64 {
	if v, ok := m[name]; ok {
		if f, err := valueFloat(v); err == nil {
			return f
		}
	}
	return 0
}

// fieldBool returns field name's integer form as a boolean (nonzero = true),
// or false when absent.
func fieldBool(m map[string]Value, name string) bool {
	return fieldInt(m, name) != 0
}

// --- MapStringValueCmd: for map replies with heterogeneous value types (e.g. XINFO STREAM) ---

// MapStringValueCmd holds the result of a command that returns a map whose
// values are not all the same scalar type.
type MapStringValueCmd struct {
	Cmd
}

// NewMapStringValueCmd creates a MapStringValueCmd with the given error.
func NewMapStringValueCmd(err error) *MapStringValueCmd {
	return &MapStringValueCmd{Cmd: Cmd{err: err}}
}

// Result returns the map and error.
func (c *MapStringValueCmd) Result() (map[string]Value, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, Nil
	}
	pairs, err := valuePairs(c.val)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Value, len(pairs))
	for _, kv := range pairs {
		m[kv.Key.Str] = kv.Val
	}
	return m, nil
}

// --- ZSliceCmd: for commands returning members with scores ---

// ZSliceCmd holds the result of a command that returns members with scores
// (e.g. ZRANGE ... WITHSCORES, ZPOPMIN/ZPOPMAX, ZRANDMEMBER ... WITHSCORES).
type ZSliceCmd struct {
	Cmd
}

// NewZSliceCmd creates a ZSliceCmd with the given error.
func NewZSliceCmd(err error) *ZSliceCmd {
	return &ZSliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the scored members and error.
func (c *ZSliceCmd) Result() ([]Z, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, nil
	}
	pairs, err := valuePairSlice(c.val)
	if err != nil {
		return nil, err
	}
	out := make([]Z, len(pairs))
	for i, p := range pairs {
		score, err := valueFloat(p[1])
		if err != nil {
			return nil, err
		}
		out[i] = Z{Member: p[0].Str, Score: score}
	}
	return out, nil
}

// --- KeyValueSliceCmd: for paired field/value replies (e.g. HRANDFIELD ... WITHVALUES) ---

// KV represents one field/value pair, e.g. one HRANDFIELD ... WITHVALUES result.
type KV struct {
	Key   string
	Value string
}

// KeyValueSliceCmd holds the result of a command that returns field/value pairs.
type KeyValueSliceCmd struct {
	Cmd
}

// NewKeyValueSliceCmd creates a KeyValueSliceCmd with the given error.
func NewKeyValueSliceCmd(err error) *KeyValueSliceCmd {
	return &KeyValueSliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the field/value pairs and error.
func (c *KeyValueSliceCmd) Result() ([]KV, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, nil
	}
	pairs, err := valuePairSlice(c.val)
	if err != nil {
		return nil, err
	}
	out := make([]KV, len(pairs))
	for i, p := range pairs {
		out[i] = KV{Key: p[0].Str, Value: p[1].Str}
	}
	return out, nil
}

// --- Stream types and commands (XADD, XREAD, XINFO STREAM, ...) ---

// StreamEntry is one entry in a stream: an ID and its field/value pairs.
type StreamEntry struct {
	ID     string
	Fields map[string]string
}

func valueToStreamEntry(v Value) (StreamEntry, error) {
	if len(v.Elems) != 2 {
		return StreamEntry{}, fmt.Errorf("hotkv: expected a 2 element stream entry, got %d elements", len(v.Elems))
	}
	pairs, err := valuePairs(v.Elems[1])
	if err != nil {
		return StreamEntry{}, err
	}
	fields := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		fields[kv.Key.Str] = kv.Val.Str
	}
	return StreamEntry{ID: v.Elems[0].Str, Fields: fields}, nil
}

func valueToStreamEntries(v Value) ([]StreamEntry, error) {
	out := make([]StreamEntry, len(v.Elems))
	for i, e := range v.Elems {
		se, err := valueToStreamEntry(e)
		if err != nil {
			return nil, err
		}
		out[i] = se
	}
	return out, nil
}

// StreamEntrySliceCmd holds the result of a command that returns a plain
// list of stream entries (XRANGE, XREVRANGE).
type StreamEntrySliceCmd struct {
	Cmd
}

// NewStreamEntrySliceCmd creates a StreamEntrySliceCmd with the given error.
func NewStreamEntrySliceCmd(err error) *StreamEntrySliceCmd {
	return &StreamEntrySliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the stream entries and error.
func (c *StreamEntrySliceCmd) Result() ([]StreamEntry, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, nil
	}
	return valueToStreamEntries(c.val)
}

// XStream pairs a stream name with the entries XREAD/XREADGROUP returned for it.
type XStream struct {
	Stream  string
	Entries []StreamEntry
}

// XStreamSliceCmd holds the result of XREAD/XREADGROUP: a RESP3 native map
// of stream name to entries under HELLO 3, or an array of
// `[name, entries]` pairs under RESP2.
type XStreamSliceCmd struct {
	Cmd
}

// NewXStreamSliceCmd creates an XStreamSliceCmd with the given error.
func NewXStreamSliceCmd(err error) *XStreamSliceCmd {
	return &XStreamSliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the per-stream entries and error. Result is (nil, Nil)
// when no stream had new entries (e.g. a non-blocking read found nothing).
func (c *XStreamSliceCmd) Result() ([]XStream, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, Nil
	}

	if c.val.Typ == RespMap {
		out := make([]XStream, len(c.val.MapVal))
		for i, kv := range c.val.MapVal {
			entries, err := valueToStreamEntries(kv.Val)
			if err != nil {
				return nil, err
			}
			out[i] = XStream{Stream: kv.Key.Str, Entries: entries}
		}
		return out, nil
	}

	out := make([]XStream, len(c.val.Elems))
	for i, e := range c.val.Elems {
		if len(e.Elems) != 2 {
			return nil, fmt.Errorf("hotkv: expected a 2 element stream pair, got %d elements", len(e.Elems))
		}
		entries, err := valueToStreamEntries(e.Elems[1])
		if err != nil {
			return nil, err
		}
		out[i] = XStream{Stream: e.Elems[0].Str, Entries: entries}
	}
	return out, nil
}

// --- SliceCmd: for commands returning heterogeneous arrays ---

// SliceCmd holds the result of a command that returns a raw slice of Values.
type SliceCmd struct {
	Cmd
}

// NewSliceCmd creates a SliceCmd with the given error.
func NewSliceCmd(err error) *SliceCmd {
	return &SliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the value slice and error.
func (c *SliceCmd) Result() ([]Value, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.val.IsNil() {
		return nil, Nil
	}
	return c.val.Elems, nil
}

// --- ScanCmd: for SCAN/SSCAN/HSCAN/ZSCAN ---

// ScanCmd holds the result of a cursor-based scan command.
type ScanCmd struct {
	Cmd
	cursor uint64
	keys   []string
}

// NewScanCmd creates a ScanCmd with the given error.
func NewScanCmd(err error) *ScanCmd {
	return &ScanCmd{Cmd: Cmd{err: err}}
}

// Result returns the cursor, keys, and error.
func (c *ScanCmd) Result() (uint64, []string, error) {
	return c.cursor, c.keys, c.err
}

// SetScanResult sets the parsed scan result.
func (c *ScanCmd) SetScanResult(cursor uint64, keys []string) {
	c.cursor = cursor
	c.keys = keys
}

// --- Float32SliceCmd: for VSIM.GET ---

// Float32SliceCmd holds the result of a command returning float32 slices.
type Float32SliceCmd struct {
	Cmd
	floats []float32
}

// NewFloat32SliceCmd creates a Float32SliceCmd with the given error.
func NewFloat32SliceCmd(err error) *Float32SliceCmd {
	return &Float32SliceCmd{Cmd: Cmd{err: err}}
}

// Result returns the float32 slice and error.
func (c *Float32SliceCmd) Result() ([]float32, error) {
	return c.floats, c.err
}

// SetFloats sets the parsed float32 values.
func (c *Float32SliceCmd) SetFloats(f []float32) {
	c.floats = f
}

// --- VSIMResult: for VSIM.SEARCH ---

// VSIMSearchResult is a single result from a VSIM.SEARCH query.
type VSIMSearchResult struct {
	Key    string
	Score  float64
	Vector []float32
}

// VSIMResultCmd holds the result of a VSIM.SEARCH command.
type VSIMResultCmd struct {
	Cmd
	results []VSIMSearchResult
}

// NewVSIMResultCmd creates a VSIMResultCmd with the given error.
func NewVSIMResultCmd(err error) *VSIMResultCmd {
	return &VSIMResultCmd{Cmd: Cmd{err: err}}
}

// Result returns the search results and error.
func (c *VSIMResultCmd) Result() ([]VSIMSearchResult, error) {
	return c.results, c.err
}

// SetResults sets the parsed search results.
func (c *VSIMResultCmd) SetResults(r []VSIMSearchResult) {
	c.results = r
}

// --- VSIMInfoResult: for VSIM.INFO ---

// VSIMInfoResult holds index metadata from VSIM.INFO <index>, matching the
// server's field names exactly (name, dimensions, metric, vectors, M,
// ef_construction, ef_search).
type VSIMInfoResult struct {
	Name           string
	Dimensions     int
	Metric         string
	Vectors        int64
	M              int
	EFConstruction int
	EFSearch       int
}

// VSIMInfoCmd holds the result of a VSIM.INFO command.
type VSIMInfoCmd struct {
	Cmd
	info VSIMInfoResult
}

// NewVSIMInfoCmd creates a VSIMInfoCmd with the given error.
func NewVSIMInfoCmd(err error) *VSIMInfoCmd {
	return &VSIMInfoCmd{Cmd: Cmd{err: err}}
}

// Result returns the index info and error.
func (c *VSIMInfoCmd) Result() (VSIMInfoResult, error) {
	return c.info, c.err
}

// SetInfo sets the parsed info result.
func (c *VSIMInfoCmd) SetInfo(info VSIMInfoResult) {
	c.info = info
}

// --- GCRAResult: for GCRA.LIMIT ---

// GCRAResult is the decoded reply of GCRA.LIMIT.
type GCRAResult struct {
	Denied       bool
	Limit        int64
	Remaining    int64
	RetryAfterMs int64 // -1 when Denied is false
	ResetAfterMs int64
}

// GCRACmd holds the result of a GCRA.LIMIT command.
type GCRACmd struct {
	Cmd
	res GCRAResult
}

// NewGCRACmd creates a GCRACmd with the given error.
func NewGCRACmd(err error) *GCRACmd {
	return &GCRACmd{Cmd: Cmd{err: err}}
}

// Result returns the decoded GCRA result and error.
func (c *GCRACmd) Result() (GCRAResult, error) {
	return c.res, c.err
}

// SetResult sets the decoded GCRA result.
func (c *GCRACmd) SetResult(r GCRAResult) {
	c.res = r
}

// --- RateLimitResult: for RATELIMIT.CHECK / RATELIMIT.PEEK ---

// RateLimitResult is the decoded reply of RATELIMIT.CHECK or RATELIMIT.PEEK.
type RateLimitResult struct {
	Allowed      bool
	Remaining    int64
	RetryAfterMs int64
	Limit        int64
	Current      int64
}

// RateLimitCmd holds the result of a RATELIMIT.CHECK/PEEK command.
type RateLimitCmd struct {
	Cmd
	res RateLimitResult
}

// NewRateLimitCmd creates a RateLimitCmd with the given error.
func NewRateLimitCmd(err error) *RateLimitCmd {
	return &RateLimitCmd{Cmd: Cmd{err: err}}
}

// Result returns the decoded rate limit result and error.
func (c *RateLimitCmd) Result() (RateLimitResult, error) {
	return c.res, c.err
}

// SetResult sets the decoded rate limit result.
func (c *RateLimitCmd) SetResult(r RateLimitResult) {
	c.res = r
}

// --- SCacheResult: for SCACHE.GET ---

// SCacheResult is the decoded reply of SCACHE.GET.
type SCacheResult struct {
	Hit        bool
	Response   string
	Similarity float64
}

// SCacheGetCmd holds the result of a SCACHE.GET command.
type SCacheGetCmd struct {
	Cmd
	res SCacheResult
}

// NewSCacheGetCmd creates a SCacheGetCmd with the given error.
func NewSCacheGetCmd(err error) *SCacheGetCmd {
	return &SCacheGetCmd{Cmd: Cmd{err: err}}
}

// Result returns the decoded semantic cache result and error.
func (c *SCacheGetCmd) Result() (SCacheResult, error) {
	return c.res, c.err
}

// SetResult sets the decoded semantic cache result.
func (c *SCacheGetCmd) SetResult(r SCacheResult) {
	c.res = r
}

// --- HotKeysResult: for HOTKEYS TOP ---

// HotKeyEntry is a single entry from HOTKEYS TOP.
type HotKeyEntry struct {
	Key   string
	Count int64
}

// HotKeysCmd holds the result of a HOTKEYS TOP command.
type HotKeysCmd struct {
	Cmd
	keys []HotKeyEntry
}

// NewHotKeysCmd creates a HotKeysCmd with the given error.
func NewHotKeysCmd(err error) *HotKeysCmd {
	return &HotKeysCmd{Cmd: Cmd{err: err}}
}

// Result returns the hot key entries and error.
func (c *HotKeysCmd) Result() ([]HotKeyEntry, error) {
	return c.keys, c.err
}

// SetKeys sets the parsed hot key entries.
func (c *HotKeysCmd) SetKeys(keys []HotKeyEntry) {
	c.keys = keys
}

// --- UsageSavings: the usage_saved_* fields shared by SCacheStats,
// PCacheStats and the keyed caches' Stats ---

// UsageSavings holds the tokens (and, once the model is priced, the cost) a
// cache's hits did not have to spend, accumulated from usage recorded on a
// SET call (see e.g. PCacheSet's usage parameter). Every field is zero when
// no SET ever carried a usage value.
type UsageSavings struct {
	Requests         int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
}

func parseUsageSavings(m map[string]Value) UsageSavings {
	return UsageSavings{
		Requests:         fieldInt(m, "usage_saved_requests"),
		InputTokens:      fieldInt(m, "usage_saved_input_tokens"),
		OutputTokens:     fieldInt(m, "usage_saved_output_tokens"),
		CacheReadTokens:  fieldInt(m, "usage_saved_cache_read_tokens"),
		CacheWriteTokens: fieldInt(m, "usage_saved_cache_write_tokens"),
		CostUSD:          fieldFloat(m, "usage_saved_cost_usd"),
	}
}

// --- SCacheStatsResult: for SCACHE.STATS ---

// SCacheStatsResult is the decoded reply of SCACHE.STATS.
type SCacheStatsResult struct {
	Entries int64
	Hits    int64
	Misses  int64
	HitRate float64
	P50Us   int64
	Usage   UsageSavings
}

// SCacheStatsCmd holds the result of a SCACHE.STATS command.
type SCacheStatsCmd struct {
	Cmd
	res SCacheStatsResult
}

// NewSCacheStatsCmd creates a SCacheStatsCmd with the given error.
func NewSCacheStatsCmd(err error) *SCacheStatsCmd { return &SCacheStatsCmd{Cmd: Cmd{err: err}} }

// Result returns the decoded stats and error.
func (c *SCacheStatsCmd) Result() (SCacheStatsResult, error) { return c.res, c.err }

// SetResult sets the decoded stats.
func (c *SCacheStatsCmd) SetResult(r SCacheStatsResult) { c.res = r }

func parseSCacheStats(v Value) (SCacheStatsResult, error) {
	m, err := namedFields(v)
	if err != nil {
		return SCacheStatsResult{}, err
	}
	return SCacheStatsResult{
		Entries: fieldInt(m, "entries"),
		Hits:    fieldInt(m, "hits"),
		Misses:  fieldInt(m, "misses"),
		HitRate: fieldFloat(m, "hit_rate"),
		P50Us:   fieldInt(m, "p50_us"),
		Usage:   parseUsageSavings(m),
	}, nil
}

// --- PCacheStatsResult: for PCACHE.STATS ---

// PCacheStatsResult is the decoded reply of PCACHE.STATS.
type PCacheStatsResult struct {
	Entries      int64
	Hits         int64
	Misses       int64
	Evictions    int64
	MemoryBytes  int64
	HitRate      float64
	P50Ns        int64
	P95Ns        int64
	P99Ns        int64
	CostSavedUSD float64
	Usage        UsageSavings
}

// PCacheStatsCmd holds the result of a PCACHE.STATS command.
type PCacheStatsCmd struct {
	Cmd
	res PCacheStatsResult
}

// NewPCacheStatsCmd creates a PCacheStatsCmd with the given error.
func NewPCacheStatsCmd(err error) *PCacheStatsCmd { return &PCacheStatsCmd{Cmd: Cmd{err: err}} }

// Result returns the decoded stats and error.
func (c *PCacheStatsCmd) Result() (PCacheStatsResult, error) { return c.res, c.err }

// SetResult sets the decoded stats.
func (c *PCacheStatsCmd) SetResult(r PCacheStatsResult) { c.res = r }

func parsePCacheStats(v Value) (PCacheStatsResult, error) {
	m, err := namedFields(v)
	if err != nil {
		return PCacheStatsResult{}, err
	}
	return PCacheStatsResult{
		Entries:      fieldInt(m, "entries"),
		Hits:         fieldInt(m, "hits"),
		Misses:       fieldInt(m, "misses"),
		Evictions:    fieldInt(m, "evictions"),
		MemoryBytes:  fieldInt(m, "memory_bytes"),
		HitRate:      fieldFloat(m, "hit_rate"),
		P50Ns:        fieldInt(m, "p50_ns"),
		P95Ns:        fieldInt(m, "p95_ns"),
		P99Ns:        fieldInt(m, "p99_ns"),
		CostSavedUSD: fieldFloat(m, "cost_saved_usd"),
		Usage:        parseUsageSavings(m),
	}, nil
}

// --- KeyedCacheStatsResult: for TOOL.CACHE/NCACHE/GUARD/RERANK .STATS ---

// KeyedCacheStatsResult is the decoded reply of a keyed cache's STATS
// command (ToolCacheStats, NCacheStats, GuardCacheStats, RerankCacheStats).
type KeyedCacheStatsResult struct {
	Entries     int64
	Hits        int64
	Misses      int64
	Evictions   int64
	MemoryBytes int64
	Usage       UsageSavings
}

// KeyedCacheStatsCmd holds the result of a keyed cache STATS command.
type KeyedCacheStatsCmd struct {
	Cmd
	res KeyedCacheStatsResult
}

// NewKeyedCacheStatsCmd creates a KeyedCacheStatsCmd with the given error.
func NewKeyedCacheStatsCmd(err error) *KeyedCacheStatsCmd {
	return &KeyedCacheStatsCmd{Cmd: Cmd{err: err}}
}

// Result returns the decoded stats and error.
func (c *KeyedCacheStatsCmd) Result() (KeyedCacheStatsResult, error) { return c.res, c.err }

// SetResult sets the decoded stats.
func (c *KeyedCacheStatsCmd) SetResult(r KeyedCacheStatsResult) { c.res = r }

func parseKeyedCacheStats(v Value) (KeyedCacheStatsResult, error) {
	m, err := namedFields(v)
	if err != nil {
		return KeyedCacheStatsResult{}, err
	}
	return KeyedCacheStatsResult{
		Entries:     fieldInt(m, "entries"),
		Hits:        fieldInt(m, "hits"),
		Misses:      fieldInt(m, "misses"),
		Evictions:   fieldInt(m, "evictions"),
		MemoryBytes: fieldInt(m, "memory_bytes"),
		Usage:       parseUsageSavings(m),
	}, nil
}

// --- LLMStatsResult: for LLMSTATS.GET ---

// LLMStatsResult is the decoded reply of LLMSTATS.GET.
type LLMStatsResult struct {
	Requests           int64
	Errors             int64
	InputTokens        int64
	OutputTokens       int64
	CachedTokens       int64
	CostUSD            float64
	AvgLatencyMs       float64
	Requests1m         int64
	Tokens1m           int64
	CostUSD1m          float64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	UnpricedRequests   int64
}

// LLMStatsCmd holds the result of an LLMSTATS.GET command.
type LLMStatsCmd struct {
	Cmd
	res LLMStatsResult
}

// NewLLMStatsCmd creates an LLMStatsCmd with the given error.
func NewLLMStatsCmd(err error) *LLMStatsCmd { return &LLMStatsCmd{Cmd: Cmd{err: err}} }

// Result returns the decoded stats and error.
func (c *LLMStatsCmd) Result() (LLMStatsResult, error) { return c.res, c.err }

// SetResult sets the decoded stats.
func (c *LLMStatsCmd) SetResult(r LLMStatsResult) { c.res = r }

func parseLLMStats(v Value) (LLMStatsResult, error) {
	m, err := namedFields(v)
	if err != nil {
		return LLMStatsResult{}, err
	}
	return LLMStatsResult{
		Requests:           fieldInt(m, "requests"),
		Errors:             fieldInt(m, "errors"),
		InputTokens:        fieldInt(m, "input_tokens"),
		OutputTokens:       fieldInt(m, "output_tokens"),
		CachedTokens:       fieldInt(m, "cached_tokens"),
		CostUSD:            fieldFloat(m, "cost_usd"),
		AvgLatencyMs:       fieldFloat(m, "avg_latency_ms"),
		Requests1m:         fieldInt(m, "requests_1m"),
		Tokens1m:           fieldInt(m, "tokens_1m"),
		CostUSD1m:          fieldFloat(m, "cost_usd_1m"),
		CacheWrite5mTokens: fieldInt(m, "cache_write_5m_tokens"),
		CacheWrite1hTokens: fieldInt(m, "cache_write_1h_tokens"),
		UnpricedRequests:   fieldInt(m, "unpriced_requests"),
	}, nil
}

// --- LLMBudgetStatusResult: for LLMBUDGET.STATUS ---

// LLMBudgetStatusResult is the decoded reply of LLMBUDGET.STATUS.
type LLMBudgetStatusResult struct {
	TokensUsed       int64
	TokenLimit       int64
	CostUsedUSD      float64
	CostLimitUSD     float64
	WindowSeconds    int64
	WindowRemaining  int64
	RequestsAllowed  int64
	RequestsRejected int64
	UnpricedRequests int64
}

// LLMBudgetStatusCmd holds the result of an LLMBUDGET.STATUS command.
type LLMBudgetStatusCmd struct {
	Cmd
	res LLMBudgetStatusResult
}

// NewLLMBudgetStatusCmd creates an LLMBudgetStatusCmd with the given error.
func NewLLMBudgetStatusCmd(err error) *LLMBudgetStatusCmd {
	return &LLMBudgetStatusCmd{Cmd: Cmd{err: err}}
}

// Result returns the decoded status and error.
func (c *LLMBudgetStatusCmd) Result() (LLMBudgetStatusResult, error) { return c.res, c.err }

// SetResult sets the decoded status.
func (c *LLMBudgetStatusCmd) SetResult(r LLMBudgetStatusResult) { c.res = r }

func parseLLMBudgetStatus(v Value) (LLMBudgetStatusResult, error) {
	m, err := namedFields(v)
	if err != nil {
		return LLMBudgetStatusResult{}, err
	}
	return LLMBudgetStatusResult{
		TokensUsed:       fieldInt(m, "tokens_used"),
		TokenLimit:       fieldInt(m, "token_limit"),
		CostUsedUSD:      fieldFloat(m, "cost_used_usd"),
		CostLimitUSD:     fieldFloat(m, "cost_limit_usd"),
		WindowSeconds:    fieldInt(m, "window_seconds"),
		WindowRemaining:  fieldInt(m, "window_remaining"),
		RequestsAllowed:  fieldInt(m, "requests_allowed"),
		RequestsRejected: fieldInt(m, "requests_rejected"),
		UnpricedRequests: fieldInt(m, "unpriced_requests"),
	}, nil
}

// --- LLMPriceResult: for LLMSTATS.PRICE GET/LIST ---

// LLMPriceResult is one model's decoded price sheet, as returned by
// LLMSTATS.PRICE GET/LIST. Source is "default", "custom" or "unpriced".
type LLMPriceResult struct {
	Model                  string
	Source                 string
	Priced                 bool
	Retired                bool
	InputUSDPerMtok        float64
	OutputUSDPerMtok       float64
	CacheReadUSDPerMtok    float64
	CacheWrite5mUSDPerMtok float64
	CacheWrite1hUSDPerMtok float64
}

// LLMPriceCmd holds the result of an LLMSTATS.PRICE GET command.
type LLMPriceCmd struct {
	Cmd
	res LLMPriceResult
}

// NewLLMPriceCmd creates an LLMPriceCmd with the given error.
func NewLLMPriceCmd(err error) *LLMPriceCmd { return &LLMPriceCmd{Cmd: Cmd{err: err}} }

// Result returns the decoded price sheet and error.
func (c *LLMPriceCmd) Result() (LLMPriceResult, error) { return c.res, c.err }

// SetResult sets the decoded price sheet.
func (c *LLMPriceCmd) SetResult(r LLMPriceResult) { c.res = r }

// LLMPriceSliceCmd holds the result of an LLMSTATS.PRICE LIST command.
type LLMPriceSliceCmd struct {
	Cmd
	res []LLMPriceResult
}

// NewLLMPriceSliceCmd creates an LLMPriceSliceCmd with the given error.
func NewLLMPriceSliceCmd(err error) *LLMPriceSliceCmd { return &LLMPriceSliceCmd{Cmd: Cmd{err: err}} }

// Result returns the decoded price sheets and error.
func (c *LLMPriceSliceCmd) Result() ([]LLMPriceResult, error) { return c.res, c.err }

// SetResult sets the decoded price sheets.
func (c *LLMPriceSliceCmd) SetResult(r []LLMPriceResult) { c.res = r }

func parseLLMPrice(v Value) (LLMPriceResult, error) {
	m, err := namedFields(v)
	if err != nil {
		return LLMPriceResult{}, err
	}
	return LLMPriceResult{
		Model:                  fieldStr(m, "model"),
		Source:                 fieldStr(m, "source"),
		Priced:                 fieldBool(m, "priced"),
		Retired:                fieldBool(m, "retired"),
		InputUSDPerMtok:        fieldFloat(m, "input_usd_per_mtok"),
		OutputUSDPerMtok:       fieldFloat(m, "output_usd_per_mtok"),
		CacheReadUSDPerMtok:    fieldFloat(m, "cache_read_usd_per_mtok"),
		CacheWrite5mUSDPerMtok: fieldFloat(m, "cache_write_5m_usd_per_mtok"),
		CacheWrite1hUSDPerMtok: fieldFloat(m, "cache_write_1h_usd_per_mtok"),
	}, nil
}
