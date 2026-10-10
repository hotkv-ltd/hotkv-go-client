// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package proto

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// CommandWriter is a convenience wrapper that serialises RESP commands.
// It is kept separate for clarity; the core Writer handles generic RESP.
type CommandWriter struct {
	wr  *bufio.Writer
	buf []byte
}

// NewCommandWriter creates a new command writer.
func NewCommandWriter(w io.Writer) *CommandWriter {
	return &CommandWriter{
		wr:  bufio.NewWriterSize(w, 64*1024),
		buf: make([]byte, 0, 64),
	}
}

// Flush flushes the buffer.
func (cw *CommandWriter) Flush() error { return cw.wr.Flush() }

// Buffered returns the number of unflushed bytes.
func (cw *CommandWriter) Buffered() int { return cw.wr.Buffered() }

// WriteCommand writes a RESP array command from string args.
func (cw *CommandWriter) WriteCommand(args ...string) error {
	cw.buf = cw.buf[:0]
	cw.buf = append(cw.buf, '*')
	cw.buf = strconv.AppendInt(cw.buf, int64(len(args)), 10)
	cw.buf = append(cw.buf, '\r', '\n')
	if _, err := cw.wr.Write(cw.buf); err != nil {
		return err
	}
	for _, a := range args {
		cw.buf = cw.buf[:0]
		cw.buf = append(cw.buf, '$')
		cw.buf = strconv.AppendInt(cw.buf, int64(len(a)), 10)
		cw.buf = append(cw.buf, '\r', '\n')
		if _, err := cw.wr.Write(cw.buf); err != nil {
			return err
		}
		if _, err := cw.wr.WriteString(a); err != nil {
			return err
		}
		if _, err := cw.wr.Write([]byte{'\r', '\n'}); err != nil {
			return err
		}
	}
	return nil
}

// WriteArgs writes a RESP array command from mixed-type args.
func (cw *CommandWriter) WriteArgs(args []interface{}) error {
	cw.buf = cw.buf[:0]
	cw.buf = append(cw.buf, '*')
	cw.buf = strconv.AppendInt(cw.buf, int64(len(args)), 10)
	cw.buf = append(cw.buf, '\r', '\n')
	if _, err := cw.wr.Write(cw.buf); err != nil {
		return err
	}
	for _, arg := range args {
		if err := cw.writeArg(arg); err != nil {
			return err
		}
	}
	return nil
}

func (cw *CommandWriter) writeArg(arg interface{}) error {
	var s string
	switch v := arg.(type) {
	case string:
		s = v
	case []byte:
		return cw.writeBulkBytes(v)
	case int:
		s = strconv.Itoa(v)
	case int64:
		s = strconv.FormatInt(v, 10)
	case uint64:
		s = strconv.FormatUint(v, 10)
	case float32:
		s = strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		s = strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			s = "1"
		} else {
			s = "0"
		}
	case nil:
		_, err := cw.wr.Write([]byte("$-1\r\n"))
		return err
	default:
		return fmt.Errorf("hotkv: unsupported type %T", arg)
	}
	return cw.writeBulkStr(s)
}

func (cw *CommandWriter) writeBulkStr(s string) error {
	cw.buf = cw.buf[:0]
	cw.buf = append(cw.buf, '$')
	cw.buf = strconv.AppendInt(cw.buf, int64(len(s)), 10)
	cw.buf = append(cw.buf, '\r', '\n')
	if _, err := cw.wr.Write(cw.buf); err != nil {
		return err
	}
	if _, err := cw.wr.WriteString(s); err != nil {
		return err
	}
	_, err := cw.wr.Write([]byte{'\r', '\n'})
	return err
}

func (cw *CommandWriter) writeBulkBytes(b []byte) error {
	cw.buf = cw.buf[:0]
	cw.buf = append(cw.buf, '$')
	cw.buf = strconv.AppendInt(cw.buf, int64(len(b)), 10)
	cw.buf = append(cw.buf, '\r', '\n')
	if _, err := cw.wr.Write(cw.buf); err != nil {
		return err
	}
	if _, err := cw.wr.Write(b); err != nil {
		return err
	}
	_, err := cw.wr.Write([]byte{'\r', '\n'})
	return err
}
