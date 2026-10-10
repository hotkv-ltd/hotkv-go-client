// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"errors"
	"fmt"
	"strings"
)

// Nil is returned when HotKV returns a null bulk string or null array.
var Nil = errors.New("hotkv: nil")

// TxFailedErr is returned when EXEC fails because a watched key was modified.
var TxFailedErr = errors.New("hotkv: transaction failed")

// ErrClosed is returned when performing operations on a closed client.
var ErrClosed = errors.New("hotkv: client is closed")

// ErrPoolTimeout is returned when a connection cannot be obtained within the timeout.
var ErrPoolTimeout = errors.New("hotkv: connection pool timeout")

// ErrPoolExhausted is returned when the pool has no available connections.
var ErrPoolExhausted = errors.New("hotkv: connection pool exhausted")

// Error represents a server error returned as a RESP error reply.
type Error struct {
	prefix string
	msg    string
	raw    string
}

// NewError creates an Error from a raw RESP error string (e.g. "ERR unknown command").
func NewError(raw string) *Error {
	prefix, msg, _ := strings.Cut(raw, " ")
	return &Error{prefix: prefix, msg: msg, raw: raw}
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("hotkv: %s", e.raw)
}

// Prefix returns the error prefix (e.g. "ERR", "WRONGTYPE", "NOPERM").
func (e *Error) Prefix() string {
	return e.prefix
}

// Message returns the error message after the prefix.
func (e *Error) Message() string {
	return e.msg
}

// Raw returns the original RESP error string.
func (e *Error) Raw() string {
	return e.raw
}

// IsMovedError reports whether the error is a MOVED cluster redirect.
func IsMovedError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.prefix == "MOVED"
}

// IsAskError reports whether the error is an ASK cluster redirect.
func IsAskError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.prefix == "ASK"
}

// IsReadOnlyError reports whether the error indicates a read-only replica.
func IsReadOnlyError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.prefix == "READONLY"
}

// IsNoAuthError reports whether the error is a NOAUTH error.
func IsNoAuthError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.prefix == "NOAUTH"
}

// IsWrongTypeError reports whether the error is a WRONGTYPE error.
func IsWrongTypeError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.prefix == "WRONGTYPE"
}

// IsNoLicenseError reports whether the error is a NOLICENSE error: an
// enterprise/AI command rejected because the server's currently loaded
// license does not cover that feature. Tests and callers should treat this
// distinctly from other server errors, e.g. by skipping rather than failing.
func IsNoLicenseError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.prefix == "NOLICENSE"
}
