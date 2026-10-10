// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package proto

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Type identifies the RESP wire type of a parsed value.
type Type int

const (
	SimpleString Type = iota
	Error
	Integer
	BulkString
	Array
	Null
	Double
	Boolean
	BlobError
	VerbatimStr
	BigNumber
	Map
	Set
	Attribute
	Push
)

// Value represents a single parsed RESP value.
type Value struct {
	Typ    Type
	Str    string
	Int    int64
	Dbl    float64
	Bool   bool
	Attrs  *Value
	Elems  []Value
	MapVal []KeyValue
}

// KeyValue is a key-value pair in a RESP3 map.
type KeyValue struct {
	Key Value
	Val Value
}

// IsNil reports whether the value is a RESP null.
func (v Value) IsNil() bool { return v.Typ == Null }

// IsErr reports whether the value is an error.
func (v Value) IsErr() bool { return v.Typ == Error || v.Typ == BlobError }

// BigInt parses the BigNumber string into a *big.Int.
func (v Value) BigInt() *big.Int {
	if v.Typ != BigNumber {
		return nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(v.Str, 10); !ok {
		return nil
	}
	return n
}

const (
	maxBulkLen   = 512 * 1024 * 1024
	maxArrayLen  = 1048576
	maxNestDepth = 64
)

// Reader reads RESP2/RESP3 values from a buffered stream.
type Reader struct {
	rd *bufio.Reader
}

// NewReader creates a new RESP protocol reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{rd: bufio.NewReaderSize(r, 64*1024)}
}

// ReadValue reads the next complete RESP value.
func (r *Reader) ReadValue() (Value, error) {
	return r.readValue(0)
}

func (r *Reader) readValue(depth int) (Value, error) {
	if depth > maxNestDepth {
		return Value{}, fmt.Errorf("hotkv: RESP nesting depth exceeded %d", maxNestDepth)
	}
	b, err := r.rd.ReadByte()
	if err != nil {
		return Value{}, err
	}
	switch b {
	case '+':
		return r.readSimple(SimpleString)
	case '-':
		return r.readSimple(Error)
	case ':':
		return r.readInt()
	case '$':
		return r.readBulk()
	case '*':
		return r.readArray(depth)
	case '_':
		return r.readNull()
	case ',':
		return r.readDouble()
	case '#':
		return r.readBool()
	case '!':
		return r.readBlob(BlobError)
	case '=':
		return r.readBlob(VerbatimStr)
	case '(':
		return r.readBigNum()
	case '%':
		return r.readMap(depth)
	case '~':
		return r.readSet(depth)
	case '|':
		return r.readAttr(depth)
	case '>':
		return r.readPush(depth)
	default:
		if err := r.rd.UnreadByte(); err != nil {
			return Value{}, err
		}
		return r.readInline()
	}
}

func (r *Reader) readLine() (string, error) {
	line, err := r.rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return "", fmt.Errorf("hotkv: invalid line terminator")
	}
	return line[:len(line)-2], nil
}

func (r *Reader) readSimple(t Type) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	return Value{Typ: t, Str: line}, nil
}

func (r *Reader) readInt() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	n, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid integer %q: %w", line, err)
	}
	return Value{Typ: Integer, Int: n}, nil
}

func (r *Reader) readBulk() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	length, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid bulk length %q: %w", line, err)
	}
	if length == -1 {
		return Value{Typ: Null}, nil
	}
	if length < 0 || length > maxBulkLen {
		return Value{}, fmt.Errorf("hotkv: bulk length %d out of range", length)
	}
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(r.rd, buf); err != nil {
		return Value{}, err
	}
	if buf[length] != '\r' || buf[length+1] != '\n' {
		return Value{}, fmt.Errorf("hotkv: missing CRLF after bulk string")
	}
	return Value{Typ: BulkString, Str: string(buf[:length])}, nil
}

func (r *Reader) readArray(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid array count %q: %w", line, err)
	}
	if count == -1 {
		return Value{Typ: Null}, nil
	}
	if count < 0 || count > maxArrayLen {
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
	return Value{Typ: Array, Elems: elems}, nil
}

func (r *Reader) readNull() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	if line != "" {
		return Value{}, fmt.Errorf("hotkv: unexpected data after null: %q", line)
	}
	return Value{Typ: Null}, nil
}

func (r *Reader) readDouble() (Value, error) {
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
	return Value{Typ: Double, Dbl: d}, nil
}

func (r *Reader) readBool() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	switch line {
	case "t":
		return Value{Typ: Boolean, Bool: true}, nil
	case "f":
		return Value{Typ: Boolean, Bool: false}, nil
	default:
		return Value{}, fmt.Errorf("hotkv: invalid boolean %q", line)
	}
}

func (r *Reader) readBlob(t Type) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	length, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid blob length %q: %w", line, err)
	}
	if length < 0 || length > maxBulkLen {
		return Value{}, fmt.Errorf("hotkv: blob length %d out of range", length)
	}
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(r.rd, buf); err != nil {
		return Value{}, err
	}
	if buf[length] != '\r' || buf[length+1] != '\n' {
		return Value{}, fmt.Errorf("hotkv: missing CRLF after blob")
	}
	return Value{Typ: t, Str: string(buf[:length])}, nil
}

func (r *Reader) readBigNum() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	if _, ok := new(big.Int).SetString(line, 10); !ok {
		return Value{}, fmt.Errorf("hotkv: invalid big number %q", line)
	}
	return Value{Typ: BigNumber, Str: line}, nil
}

func (r *Reader) readMap(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid map count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayLen/2 {
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
	return Value{Typ: Map, MapVal: pairs}, nil
}

func (r *Reader) readSet(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid set count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayLen {
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
	return Value{Typ: Set, Elems: elems}, nil
}

func (r *Reader) readAttr(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid attribute count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayLen/2 {
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
	attr := Value{Typ: Attribute, MapVal: pairs}
	val, err := r.readValue(depth)
	if err != nil {
		return Value{}, err
	}
	val.Attrs = &attr
	return val, nil
}

func (r *Reader) readPush(depth int) (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return Value{}, fmt.Errorf("hotkv: invalid push count %q: %w", line, err)
	}
	if count < 0 || count > maxArrayLen {
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
	return Value{Typ: Push, Elems: elems}, nil
}

func (r *Reader) readInline() (Value, error) {
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
		elems[i] = Value{Typ: BulkString, Str: p}
	}
	return Value{Typ: Array, Elems: elems}, nil
}

// Writer writes RESP protocol data to a buffered stream.
type Writer struct {
	wr  *bufio.Writer
	buf []byte
}

// NewWriter creates a new RESP protocol writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{
		wr:  bufio.NewWriterSize(w, 64*1024),
		buf: make([]byte, 0, 64),
	}
}

// Flush flushes the internal buffer.
func (w *Writer) Flush() error { return w.wr.Flush() }

// WriteCommand writes a RESP array command.
func (w *Writer) WriteCommand(args ...string) error {
	if err := w.writeArrayHeader(len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if err := w.writeBulk(arg); err != nil {
			return err
		}
	}
	return nil
}

// WriteArgs writes a command from a mixed slice.
func (w *Writer) WriteArgs(args []interface{}) error {
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

func (w *Writer) writeArg(arg interface{}) error {
	switch v := arg.(type) {
	case string:
		return w.writeBulk(v)
	case []byte:
		return w.writeBulkBytes(v)
	case int:
		return w.writeBulk(strconv.Itoa(v))
	case int64:
		return w.writeBulk(strconv.FormatInt(v, 10))
	case uint64:
		return w.writeBulk(strconv.FormatUint(v, 10))
	case float32:
		return w.writeBulk(strconv.FormatFloat(float64(v), 'f', -1, 32))
	case float64:
		return w.writeBulk(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		if v {
			return w.writeBulk("1")
		}
		return w.writeBulk("0")
	case nil:
		_, err := w.wr.Write([]byte("$-1\r\n"))
		return err
	default:
		return fmt.Errorf("hotkv: unsupported type %T", arg)
	}
}

func (w *Writer) writeArrayHeader(n int) error {
	w.buf = w.buf[:0]
	w.buf = append(w.buf, '*')
	w.buf = strconv.AppendInt(w.buf, int64(n), 10)
	w.buf = append(w.buf, '\r', '\n')
	_, err := w.wr.Write(w.buf)
	return err
}

func (w *Writer) writeBulk(s string) error {
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

func (w *Writer) writeBulkBytes(b []byte) error {
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
