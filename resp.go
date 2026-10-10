// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// RespType identifies the RESP wire type of a parsed value.
type RespType int

const (
	// RESP2 types.
	RespSimpleString RespType = iota // +
	RespError                        // -
	RespInteger                      // :
	RespBulkString                   // $
	RespArray                        // *

	// RESP3 types.
	RespNull        // _
	RespDouble      // ,
	RespBoolean     // #
	RespBlobError   // !
	RespVerbatimStr // =
	RespBigNumber   // (
	RespMap         // %
	RespSet         // ~
	RespAttribute   // |
	RespPush        // >
)

// Value represents a single parsed RESP value from the wire.
type Value struct {
	Typ    RespType
	Str    string     // SimpleString, Error, BulkString, BlobError, VerbatimStr, BigNumber raw
	Int    int64      // Integer
	Dbl    float64    // Double
	Bool   bool       // Boolean
	Attrs  *Value     // RESP3 attribute (precedes the real value)
	Elems  []Value    // Array, Set, Push
	MapVal []KeyValue // Map
}

// KeyValue is a key-value pair inside a RESP3 map.
type KeyValue struct {
	Key Value
	Val Value
}

// IsNil returns true when the value represents a RESP null.
func (v Value) IsNil() bool {
	return v.Typ == RespNull
}

// IsErr returns true when the value is an error (RESP2 or RESP3 blob error).
func (v Value) IsErr() bool {
	return v.Typ == RespError || v.Typ == RespBlobError
}

// BigInt parses the BigNumber string into a *big.Int. Returns nil on failure.
func (v Value) BigInt() *big.Int {
	if v.Typ != RespBigNumber {
		return nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(v.Str, 10); !ok {
		return nil
	}
	return n
}

// VerbatimEncoding returns the 3-byte encoding prefix of a verbatim string
// (e.g. "txt" or "mkd"). Returns empty string for non-verbatim values.
func (v Value) VerbatimEncoding() string {
	if v.Typ != RespVerbatimStr && len(v.Str) >= 4 && v.Str[3] == ':' {
		return ""
	}
	if v.Typ == RespVerbatimStr && len(v.Str) >= 4 && v.Str[3] == ':' {
		return v.Str[:3]
	}
	return ""
}

// VerbatimContent returns the content portion of a verbatim string (after "enc:").
func (v Value) VerbatimContent() string {
	if v.Typ == RespVerbatimStr && len(v.Str) >= 4 && v.Str[3] == ':' {
		return v.Str[4:]
	}
	return v.Str
}

// RESP protocol constants.
const (
	maxBulkLength    = 512 * 1024 * 1024 // 512 MB, matches server kMaxBulkLength
	maxArrayElements = 1048576           // 1 M, matches server kMaxArrayElements
	maxNestingDepth  = 64
)

// RespReader reads RESP2 and RESP3 values from a buffered stream.
type RespReader struct {
	rd *bufio.Reader
}

// NewRespReader wraps an io.Reader with buffered RESP parsing.
func NewRespReader(r io.Reader) *RespReader {
	return &RespReader{rd: bufio.NewReaderSize(r, 64*1024)}
}

// NewRespReaderSize wraps an io.Reader with the given buffer size.
func NewRespReaderSize(r io.Reader, size int) *RespReader {
	return &RespReader{rd: bufio.NewReaderSize(r, size)}
}

// ReadValue reads the next complete RESP value from the stream.
func (r *RespReader) ReadValue() (Value, error) {
	return r.readValue(0)
}

func (r *RespReader) readValue(depth int) (Value, error) {
	if depth > maxNestingDepth {
		return Value{}, fmt.Errorf("hotkv: RESP nesting depth exceeded %d", maxNestingDepth)
	}

	// Peek at the type byte.
	b, err := r.rd.ReadByte()
	if err != nil {
		return Value{}, err
	}

	switch b {
	case '+':
		return r.readSimpleString()
	case '-':
		return r.readError()
	case ':':
		return r.readInteger()
	case '$':
		return r.readBulkString()
	case '*':
		return r.readArray(depth)
	case '_':
		return r.readNull()
	case ',':
		return r.readDouble()
	case '#':
		return r.readBoolean()
	case '!':
		return r.readBlobError()
	case '=':
		return r.readVerbatimString()
	case '(':
		return r.readBigNumber()
	case '%':
		return r.readMap(depth)
	case '~':
		return r.readSet(depth)
	case '|':
		return r.readAttribute(depth)
	case '>':
		return r.readPush(depth)
	default:
		// Inline command support: unget the byte and read as inline.
		if err := r.rd.UnreadByte(); err != nil {
			return Value{}, err
		}
		return r.readInline()
	}
}

// readLine reads up to \r\n and returns the line without the delimiter.
func (r *RespReader) readLine() (string, error) {
	line, err := r.rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return "", fmt.Errorf("hotkv: invalid RESP line terminator")
	}
	return line[:len(line)-2], nil
}

// --- RESP2 types ---

func (r *RespReader) readSimpleString() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	return Value{Typ: RespSimpleString, Str: line}, nil
}

func (r *RespReader) readError() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	return Value{Typ: RespError, Str: line}, nil
}

func (r *RespReader) readInteger() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	n, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid integer %q: %w", line, err)
	}
	return Value{Typ: RespInteger, Int: n}, nil
}

func (r *RespReader) readBulkString() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	length, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid bulk length %q: %w", line, err)
	}

	// Null bulk string.
	if length == -1 {
		return Value{Typ: RespNull}, nil
	}
	if length < 0 || length > maxBulkLength {
		return Value{}, fmt.Errorf("hotkv: bulk string length %d out of range", length)
	}

	// Read exactly length bytes + \r\n.
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(r.rd, buf); err != nil {
		return Value{}, err
	}
	if buf[length] != '\r' || buf[length+1] != '\n' {
		return Value{}, fmt.Errorf("hotkv: missing CRLF after bulk string")
	}
	return Value{Typ: RespBulkString, Str: string(buf[:length])}, nil
}

func (r *RespReader) readArray(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid array count %q: %w", line, err)
	}

	// Null array.
	if count == -1 {
		return Value{Typ: RespNull}, nil
	}
	if count < 0 || count > maxArrayElements {
		return Value{}, fmt.Errorf("hotkv: array count %d out of range", count)
	}

	elems := make([]Value, count)
	for i := int64(0); i < count; i++ {
		v, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		elems[i] = v
	}
	return Value{Typ: RespArray, Elems: elems}, nil
}

// --- RESP3 types ---

func (r *RespReader) readNull() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	if line != "" {
		return Value{}, fmt.Errorf("hotkv: unexpected data after null: %q", line)
	}
	return Value{Typ: RespNull}, nil
}

func (r *RespReader) readDouble() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	var d float64
	switch line {
	case "inf":
		d = math.Inf(1)
	case "-inf":
		d = math.Inf(-1)
	case "nan":
		d = math.NaN()
	default:
		d, err = strconv.ParseFloat(line, 64)
		if err != nil {
			return Value{}, fmt.Errorf("hotkv: invalid double %q: %w", line, err)
		}
	}
	return Value{Typ: RespDouble, Dbl: d}, nil
}

func (r *RespReader) readBoolean() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	switch line {
	case "t":
		return Value{Typ: RespBoolean, Bool: true}, nil
	case "f":
		return Value{Typ: RespBoolean, Bool: false}, nil
	default:
		return Value{}, fmt.Errorf("hotkv: invalid boolean %q", line)
	}
}

func (r *RespReader) readBlobError() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	length, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid blob error length %q: %w", line, err)
	}
	if length < 0 || length > maxBulkLength {
		return Value{}, fmt.Errorf("hotkv: blob error length %d out of range", length)
	}
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(r.rd, buf); err != nil {
		return Value{}, err
	}
	if buf[length] != '\r' || buf[length+1] != '\n' {
		return Value{}, fmt.Errorf("hotkv: missing CRLF after blob error")
	}
	return Value{Typ: RespBlobError, Str: string(buf[:length])}, nil
}

func (r *RespReader) readVerbatimString() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	length, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid verbatim length %q: %w", line, err)
	}
	if length < 0 || length > maxBulkLength {
		return Value{}, fmt.Errorf("hotkv: verbatim length %d out of range", length)
	}
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(r.rd, buf); err != nil {
		return Value{}, err
	}
	if buf[length] != '\r' || buf[length+1] != '\n' {
		return Value{}, fmt.Errorf("hotkv: missing CRLF after verbatim string")
	}
	return Value{Typ: RespVerbatimStr, Str: string(buf[:length])}, nil
}

func (r *RespReader) readBigNumber() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	// Validate it looks like a number.
	if _, ok := new(big.Int).SetString(line, 10); !ok {
		return Value{}, fmt.Errorf("hotkv: invalid big number %q", line)
	}
	return Value{Typ: RespBigNumber, Str: line}, nil
}

func (r *RespReader) readMap(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid map count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayElements/2 {
		return Value{}, fmt.Errorf("hotkv: map count %d out of range", count)
	}

	pairs := make([]KeyValue, count)
	for i := int64(0); i < count; i++ {
		k, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		v, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		pairs[i] = KeyValue{Key: k, Val: v}
	}
	return Value{Typ: RespMap, MapVal: pairs}, nil
}

func (r *RespReader) readSet(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid set count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayElements {
		return Value{}, fmt.Errorf("hotkv: set count %d out of range", count)
	}

	elems := make([]Value, count)
	for i := int64(0); i < count; i++ {
		v, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		elems[i] = v
	}
	return Value{Typ: RespSet, Elems: elems}, nil
}

func (r *RespReader) readAttribute(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid attribute count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayElements/2 {
		return Value{}, fmt.Errorf("hotkv: attribute count %d out of range", count)
	}

	pairs := make([]KeyValue, count)
	for i := int64(0); i < count; i++ {
		k, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		v, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		pairs[i] = KeyValue{Key: k, Val: v}
	}
	attr := Value{Typ: RespAttribute, MapVal: pairs}

	// The real value follows the attribute block.
	val, err := r.readValue(depth)
	if err != nil {
		return Value{}, err
	}
	val.Attrs = &attr
	return val, nil
}

func (r *RespReader) readPush(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid push count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayElements {
		return Value{}, fmt.Errorf("hotkv: push count %d out of range", count)
	}

	elems := make([]Value, count)
	for i := int64(0); i < count; i++ {
		v, err := r.readValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		elems[i] = v
	}
	return Value{Typ: RespPush, Elems: elems}, nil
}

func (r *RespReader) readInline() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return Value{}, fmt.Errorf("hotkv: empty inline command")
	}
	elems := make([]Value, len(parts))
	for i, p := range parts {
		elems[i] = Value{Typ: RespBulkString, Str: p}
	}
	return Value{Typ: RespArray, Elems: elems}, nil
}

// RespWriter writes RESP2/RESP3 protocol data to a buffered stream.
type RespWriter struct {
	wr  *bufio.Writer
	buf []byte // scratch buffer for integer formatting
}

// NewRespWriter wraps an io.Writer with buffered RESP writing.
func NewRespWriter(w io.Writer) *RespWriter {
	return &RespWriter{
		wr:  bufio.NewWriterSize(w, 64*1024),
		buf: make([]byte, 0, 64),
	}
}

// NewRespWriterSize wraps an io.Writer with the given buffer size.
func NewRespWriterSize(w io.Writer, size int) *RespWriter {
	return &RespWriter{
		wr:  bufio.NewWriterSize(w, size),
		buf: make([]byte, 0, 64),
	}
}

// Flush flushes the internal buffer to the underlying writer.
func (w *RespWriter) Flush() error {
	return w.wr.Flush()
}

// Buffered returns the number of bytes buffered but not yet written.
func (w *RespWriter) Buffered() int {
	return w.wr.Buffered()
}

// WriteCommand writes a RESP array command: *N\r\n $len\r\narg\r\n ...
// This is the primary method used to send commands to HotKV.
func (w *RespWriter) WriteCommand(args ...string) error {
	if err := w.writeArrayHeader(len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if err := w.writeBulkString(arg); err != nil {
			return err
		}
	}
	return nil
}

// WriteCommandBytes writes a RESP array command where arguments are byte slices.
func (w *RespWriter) WriteCommandBytes(args ...[]byte) error {
	if err := w.writeArrayHeader(len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if err := w.writeBulkBytes(arg); err != nil {
			return err
		}
	}
	return nil
}

// WriteArgs writes a RESP array command from a mixed slice of arguments.
// Supported types: string, []byte, int, int64, float64, bool.
func (w *RespWriter) WriteArgs(args []interface{}) error {
	if err := w.writeArrayHeader(len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if err := w.writeArg(arg); err != nil {
			return err
		}
	}
	return nil
}

func (w *RespWriter) writeArg(arg interface{}) error {
	switch v := arg.(type) {
	case string:
		return w.writeBulkString(v)
	case []byte:
		return w.writeBulkBytes(v)
	case int:
		return w.writeBulkString(strconv.Itoa(v))
	case int64:
		return w.writeBulkString(strconv.FormatInt(v, 10))
	case uint64:
		return w.writeBulkString(strconv.FormatUint(v, 10))
	case float32:
		return w.writeBulkString(strconv.FormatFloat(float64(v), 'f', -1, 32))
	case float64:
		return w.writeBulkString(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		if v {
			return w.writeBulkString("1")
		}
		return w.writeBulkString("0")
	case nil:
		return w.writeNullBulk()
	default:
		return fmt.Errorf("hotkv: unsupported argument type %T", arg)
	}
}

func (w *RespWriter) writeArrayHeader(n int) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, '*')
	w.buf = strconv.AppendInt(w.buf, int64(n), 10)
	w.buf = append(w.buf, '\r', '\n')
	_, err := w.wr.Write(w.buf)
	return err
}

func (w *RespWriter) writeBulkString(s string) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, '$')
	w.buf = strconv.AppendInt(w.buf, int64(len(s)), 10)
	w.buf = append(w.buf, '\r', '\n')
	if _, err := w.wr.Write(w.buf); err != nil {
		return err
	}
	if _, err := w.wr.WriteString(s); err != nil {
		return err
	}
	_, err := w.wr.Write([]byte{'\r', '\n'})
	return err
}

func (w *RespWriter) writeBulkBytes(b []byte) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, '$')
	w.buf = strconv.AppendInt(w.buf, int64(len(b)), 10)
	w.buf = append(w.buf, '\r', '\n')
	if _, err := w.wr.Write(w.buf); err != nil {
		return err
	}
	if _, err := w.wr.Write(b); err != nil {
		return err
	}
	_, err := w.wr.Write([]byte{'\r', '\n'})
	return err
}

func (w *RespWriter) writeNullBulk() error {
	_, err := w.wr.Write([]byte("$-1\r\n"))
	return err
}

// WriteSimpleString writes +str\r\n.
func (w *RespWriter) WriteSimpleString(s string) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, '+')
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, '\r', '\n')
	_, err := w.wr.Write(w.buf)
	return err
}

// WriteError writes -ERR msg\r\n.
func (w *RespWriter) WriteError(msg string) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, '-')
	w.buf = append(w.buf, msg...)
	w.buf = append(w.buf, '\r', '\n')
	_, err := w.wr.Write(w.buf)
	return err
}

// WriteInteger writes :n\r\n.
func (w *RespWriter) WriteInteger(n int64) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, ':')
	w.buf = strconv.AppendInt(w.buf, n, 10)
	w.buf = append(w.buf, '\r', '\n')
	_, err := w.wr.Write(w.buf)
	return err
}

// WriteNull writes the RESP3 null: _\r\n.
func (w *RespWriter) WriteNull() error {
	_, err := w.wr.Write([]byte("_\r\n"))
	return err
}
