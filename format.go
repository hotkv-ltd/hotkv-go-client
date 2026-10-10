// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"crypto/rand"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// This file holds the single place where Go values become command
// arguments. The server parses numeric arguments with three different
// rules, and the SDK emits one canonical spelling that all of them accept:
//
//   - formatFloat: every general "double" argument (vectors, thresholds,
//     cost-per-hit, refill/drain, time series values, INCRBYFLOAT, ...).
//     Plain decimal, shortest digits that round-trip, never an exponent,
//     never a leading '+', no locale separators, "-0" normalised to "0".
//     NaN and +/-Inf are rejected (the server only takes finite values).
//   - formatScore: sorted-set scores, bounds and weights (ZADD, ZINCRBY,
//     ZUNIONSTORE WEIGHTS, ...). Same as formatFloat, except +Inf and -Inf
//     are emitted as the literals "inf" and "-inf" the server accepts there.
//   - formatUSD: strict fixed-point USD amounts (LLMBUDGET.SET cost limit,
//     LLMSTATS.PRICE SET prices): non-negative, at most 6 fractional digits,
//     no exponent, no sign, rounded to micro-dollars. A positive amount that
//     rounds to 0 is rejected, because 0 means "no limit" for a budget.

// formatFloat formats f as a plain decimal with the shortest digits that
// round-trip. It returns an error for NaN and +/-Inf.
func formatFloat(f float64) (string, error) {
	if math.IsNaN(f) {
		return "", errors.New("hotkv: NaN is not a valid numeric argument")
	}
	if math.IsInf(f, 0) {
		return "", errors.New("hotkv: Inf is not a valid numeric argument here")
	}
	return plainDecimal(strconv.FormatFloat(f, 'f', -1, 64)), nil
}

// formatFloat32 is formatFloat for a float32, using the float32 shortest
// round-trip digits (so float32(0.1) is "0.1", not "0.10000000149011612").
func formatFloat32(f float32) (string, error) {
	v := float64(f)
	if math.IsNaN(v) {
		return "", errors.New("hotkv: NaN is not a valid numeric argument")
	}
	if math.IsInf(v, 0) {
		return "", errors.New("hotkv: Inf is not a valid numeric argument here")
	}
	return plainDecimal(strconv.FormatFloat(v, 'f', -1, 32)), nil
}

// formatScore formats a sorted-set score, bound or weight: like formatFloat
// but +Inf is "inf" and -Inf is "-inf". NaN is rejected.
func formatScore(f float64) (string, error) {
	if math.IsNaN(f) {
		return "", errors.New("hotkv: NaN is not a valid score")
	}
	if math.IsInf(f, 1) {
		return "inf", nil
	}
	if math.IsInf(f, -1) {
		return "-inf", nil
	}
	return formatFloat(f)
}

// formatUSD formats a USD amount for the server's strict fixed-point parser
// (parse_usd_to_micros): a plain non-negative decimal with a leading digit
// and at most 6 fractional digits, e.g. "12.5" or "0.00001". It rejects
// negative, NaN and Inf values, a value too large for uint64 micro-dollars,
// and a positive value that rounds to zero micro-dollars. An explicit 0 is
// formatted as "0".
func formatUSD(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("hotkv: USD amount must be finite")
	}
	if f < 0 {
		return "", errors.New("hotkv: USD amount must not be negative")
	}
	if f == 0 {
		return "0", nil
	}
	// 'f' with 6 digits rounds correctly from the exact binary value.
	s := strconv.FormatFloat(f, 'f', 6, 64)
	micros := strings.Replace(s, ".", "", 1)
	micros = strings.TrimLeft(micros, "0")
	if micros == "" {
		return "", fmt.Errorf("hotkv: USD amount %g rounds to 0 at 6 decimal places (0 would mean no limit)", f)
	}
	if _, err := strconv.ParseUint(micros, 10, 64); err != nil {
		return "", fmt.Errorf("hotkv: USD amount %g is too large", f)
	}
	return trimFraction(s), nil
}

// plainDecimal normalises negative zero and leaves everything else alone.
func plainDecimal(s string) string {
	if s == "-0" {
		return "0"
	}
	return s
}

// trimFraction drops trailing zeros (and a dangling '.') from a decimal.
func trimFraction(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

// --- deferred argument errors ---
//
// The typed command methods build []string argument lists and cannot
// return an error from the formatters above directly. A value that cannot
// be sent (NaN, an unsupported Go type, ...) is therefore encoded as a
// marker string that no real argument can equal (it embeds a per-process
// random nonce); checkArgs, run before anything is written to a socket,
// turns it back into the original error. Nothing is sent to the server.

var badArgPrefix = func() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("\x00hotkv-badarg-%d\x00", time.Now().UnixNano())
	}
	return "\x00hotkv-badarg-" + hex.EncodeToString(b[:]) + "\x00"
}()

func badArg(err error) string { return badArgPrefix + err.Error() }

// checkArgs returns the error recorded by badArg, if any argument carries one.
func checkArgs(args []string) error {
	for _, a := range args {
		if strings.HasPrefix(a, badArgPrefix) {
			return errors.New(a[len(badArgPrefix):])
		}
	}
	return nil
}

// argFloat, argFloat32, argScore and argUSD are the formatters in the
// shape the command builders use: the string, or a deferred-error marker.
func argFloat(f float64) string {
	s, err := formatFloat(f)
	if err != nil {
		return badArg(err)
	}
	return s
}

func argFloat32(f float32) string {
	s, err := formatFloat32(f)
	if err != nil {
		return badArg(err)
	}
	return s
}

func argScore(f float64) string {
	s, err := formatScore(f)
	if err != nil {
		return badArg(err)
	}
	return s
}

func argUSD(f float64) string {
	s, err := formatUSD(f)
	if err != nil {
		return badArg(err)
	}
	return s
}

// formatArg converts a Go value to a command argument: strings and byte
// slices verbatim, integers in base 10, floats with formatFloat, booleans
// as "1"/"0", and fmt.Stringer / encoding.BinaryMarshaler values through
// those methods. Any other type (including nil) is an error, never a
// silently empty argument.
func formatArg(v interface{}) string {
	s, err := convertArg(v)
	if err != nil {
		return badArg(err)
	}
	return s
}

func convertArg(v interface{}) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	case int:
		return strconv.Itoa(val), nil
	case int8:
		return strconv.FormatInt(int64(val), 10), nil
	case int16:
		return strconv.FormatInt(int64(val), 10), nil
	case int32:
		return strconv.FormatInt(int64(val), 10), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case uint:
		return strconv.FormatUint(uint64(val), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(val), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(val), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(val), 10), nil
	case uint64:
		return strconv.FormatUint(val, 10), nil
	case float32:
		return formatFloat32(val)
	case float64:
		return formatFloat(val)
	case bool:
		if val {
			return "1", nil
		}
		return "0", nil
	case encoding.BinaryMarshaler:
		b, err := val.MarshalBinary()
		if err != nil {
			return "", err
		}
		return string(b), nil
	case fmt.Stringer:
		return val.String(), nil
	case nil:
		return "", errors.New("hotkv: nil is not a valid command argument")
	default:
		return "", fmt.Errorf("hotkv: unsupported argument type %T", v)
	}
}

// convertArgs converts a mixed argument list, stopping at the first error.
func convertArgs(args []interface{}) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		s, err := convertArg(a)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// formatTimeoutSeconds formats a blocking-command timeout as decimal
// seconds, keeping sub-second precision (500ms is "0.5", not a "0" that
// would block forever).
func formatTimeoutSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}
