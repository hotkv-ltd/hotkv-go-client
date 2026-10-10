// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"
)

// --- String Commands ---

// Set sets key to value with optional expiration.
// Pass 0 for expiration to set without expiry.
func (c *Client) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"SET", key, formatArg(value)}
	if expiration > 0 {
		if usePX(expiration) {
			args = append(args, "PX", strconv.FormatInt(expiration.Milliseconds(), 10))
		} else {
			args = append(args, "EX", strconv.FormatInt(int64(expiration.Seconds()), 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SetNX sets key to value only if key does not exist. Returns true if set.
func (c *Client) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *BoolCmd {
	cmd := NewBoolCmd(nil)
	args := []string{"SET", key, formatArg(value), "NX"}
	if expiration > 0 {
		if usePX(expiration) {
			args = append(args, "PX", strconv.FormatInt(expiration.Milliseconds(), 10))
		} else {
			args = append(args, "EX", strconv.FormatInt(int64(expiration.Seconds()), 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SetXX sets key to value only if key exists.
func (c *Client) SetXX(ctx context.Context, key string, value interface{}, expiration time.Duration) *BoolCmd {
	cmd := NewBoolCmd(nil)
	args := []string{"SET", key, formatArg(value), "XX"}
	if expiration > 0 {
		if usePX(expiration) {
			args = append(args, "PX", strconv.FormatInt(expiration.Milliseconds(), 10))
		} else {
			args = append(args, "EX", strconv.FormatInt(int64(expiration.Seconds()), 10))
		}
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SetEx sets key with expiration in seconds.
func (c *Client) SetEx(ctx context.Context, key string, value interface{}, expiration time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"SETEX", key, strconv.FormatInt(int64(expiration.Seconds()), 10), formatArg(value)}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PSetEx sets key with expiration in milliseconds.
func (c *Client) PSetEx(ctx context.Context, key string, value interface{}, expiration time.Duration) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := []string{"PSETEX", key, strconv.FormatInt(expiration.Milliseconds(), 10), formatArg(value)}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// Get returns the string value of key.
func (c *Client) Get(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "GET", key)
	return cmd
}

// GetDel returns the value and deletes the key.
func (c *Client) GetDel(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "GETDEL", key)
	return cmd
}

// GetEx returns the value and optionally updates the expiration.
func (c *Client) GetEx(ctx context.Context, key string, expiration time.Duration) *StringCmd {
	cmd := NewStringCmd(nil)
	args := []string{"GETEX", key}
	if expiration > 0 {
		if usePX(expiration) {
			args = append(args, "PX", strconv.FormatInt(expiration.Milliseconds(), 10))
		} else {
			args = append(args, "EX", strconv.FormatInt(int64(expiration.Seconds()), 10))
		}
	} else if expiration == 0 {
		args = append(args, "PERSIST")
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// GetSet sets key to value and returns the old value.
func (c *Client) GetSet(ctx context.Context, key string, value interface{}) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "GETSET", key, formatArg(value))
	return cmd
}

// GetRange returns a substring of the string stored at key.
func (c *Client) GetRange(ctx context.Context, key string, start, end int64) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "GETRANGE", key, strconv.FormatInt(start, 10), strconv.FormatInt(end, 10))
	return cmd
}

// SetRange overwrites part of the string at key starting at offset.
func (c *Client) SetRange(ctx context.Context, key string, offset int64, value string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SETRANGE", key, strconv.FormatInt(offset, 10), value)
	return cmd
}

// MSet sets multiple key-value pairs.
func (c *Client) MSet(ctx context.Context, pairs ...interface{}) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := make([]string, 0, 1+len(pairs))
	args = append(args, "MSET")
	for _, p := range pairs {
		args = append(args, formatArg(p))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// MSetNX sets multiple keys only if none exist (atomic).
func (c *Client) MSetNX(ctx context.Context, pairs ...interface{}) *BoolCmd {
	cmd := NewBoolCmd(nil)
	args := make([]string, 0, 1+len(pairs))
	args = append(args, "MSETNX")
	for _, p := range pairs {
		args = append(args, formatArg(p))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// MGet returns the values of all specified keys.
func (c *Client) MGet(ctx context.Context, keys ...string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "MGET")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// Incr increments the integer value of key by 1.
func (c *Client) Incr(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "INCR", key)
	return cmd
}

// Decr decrements the integer value of key by 1.
func (c *Client) Decr(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "DECR", key)
	return cmd
}

// IncrBy increments the integer value of key by amount.
func (c *Client) IncrBy(ctx context.Context, key string, value int64) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "INCRBY", key, strconv.FormatInt(value, 10))
	return cmd
}

// DecrBy decrements the integer value of key by amount.
func (c *Client) DecrBy(ctx context.Context, key string, value int64) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "DECRBY", key, strconv.FormatInt(value, 10))
	return cmd
}

// IncrByFloat increments the float value of key by amount.
func (c *Client) IncrByFloat(ctx context.Context, key string, value float64) *FloatCmd {
	cmd := NewFloatCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "INCRBYFLOAT", key, argFloat(value))
	return cmd
}

// Append appends value to the string at key.
func (c *Client) Append(ctx context.Context, key, value string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "APPEND", key, value)
	return cmd
}

// StrLen returns the length of the string value stored at key.
func (c *Client) StrLen(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "STRLEN", key)
	return cmd
}

// --- Generic (Key) Commands ---

// Del deletes one or more keys. Returns the number of keys deleted.
func (c *Client) Del(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "DEL")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// Unlink deletes keys asynchronously.
func (c *Client) Unlink(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "UNLINK")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// Exists returns the count of how many specified keys exist.
func (c *Client) Exists(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "EXISTS")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// Expire sets an expiration on key in seconds.
func (c *Client) Expire(ctx context.Context, key string, expiration time.Duration) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EXPIRE", key, strconv.FormatInt(int64(expiration.Seconds()), 10))
	return cmd
}

// PExpire sets an expiration on key in milliseconds.
func (c *Client) PExpire(ctx context.Context, key string, expiration time.Duration) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PEXPIRE", key, strconv.FormatInt(expiration.Milliseconds(), 10))
	return cmd
}

// ExpireAt sets the expiration for a key as a UNIX timestamp (seconds).
func (c *Client) ExpireAt(ctx context.Context, key string, tm time.Time) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "EXPIREAT", key, strconv.FormatInt(tm.Unix(), 10))
	return cmd
}

// PExpireAt sets the expiration for a key as a UNIX timestamp (milliseconds).
func (c *Client) PExpireAt(ctx context.Context, key string, tm time.Time) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PEXPIREAT", key, strconv.FormatInt(tm.UnixMilli(), 10))
	return cmd
}

// ExpireTime returns the UNIX timestamp (seconds) when key expires.
func (c *Client) ExpireTime(ctx context.Context, key string) *DurationCmd {
	cmd := NewDurationCmd(time.Second, nil)
	c.processCmd(ctx, &cmd.Cmd, "EXPIRETIME", key)
	return cmd
}

// PExpireTime returns the UNIX timestamp (milliseconds) when key expires.
func (c *Client) PExpireTime(ctx context.Context, key string) *DurationCmd {
	cmd := NewDurationCmd(time.Millisecond, nil)
	c.processCmd(ctx, &cmd.Cmd, "PEXPIRETIME", key)
	return cmd
}

// TTL returns the remaining time to live of a key in seconds.
func (c *Client) TTL(ctx context.Context, key string) *DurationCmd {
	cmd := NewDurationCmd(time.Second, nil)
	c.processCmd(ctx, &cmd.Cmd, "TTL", key)
	return cmd
}

// PTTL returns the remaining time to live of a key in milliseconds.
func (c *Client) PTTL(ctx context.Context, key string) *DurationCmd {
	cmd := NewDurationCmd(time.Millisecond, nil)
	c.processCmd(ctx, &cmd.Cmd, "PTTL", key)
	return cmd
}

// Persist removes the expiration from a key.
func (c *Client) Persist(ctx context.Context, key string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PERSIST", key)
	return cmd
}

// Type returns the string representation of the type of the value stored at key.
func (c *Client) Type(ctx context.Context, key string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TYPE", key)
	return cmd
}

// Rename renames key to newkey.
func (c *Client) Rename(ctx context.Context, key, newkey string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RENAME", key, newkey)
	return cmd
}

// RenameNX renames key to newkey only if newkey does not exist.
func (c *Client) RenameNX(ctx context.Context, key, newkey string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RENAMENX", key, newkey)
	return cmd
}

// Copy copies the value from source to destination.
func (c *Client) Copy(ctx context.Context, source, destination string, replace bool) *BoolCmd {
	cmd := NewBoolCmd(nil)
	args := []string{"COPY", source, destination}
	if replace {
		args = append(args, "REPLACE")
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// Keys returns all keys matching pattern.
func (c *Client) Keys(ctx context.Context, pattern string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "KEYS", pattern)
	return cmd
}

// Scan iterates the set of keys with a cursor.
func (c *Client) Scan(ctx context.Context, cursor uint64, match string, count int64) *ScanCmd {
	cmd := NewScanCmd(nil)
	args := []string{"SCAN", strconv.FormatUint(cursor, 10)}
	if match != "" {
		args = append(args, "MATCH", match)
	}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseScanResult(cmd, val)
	return cmd
}

// RandomKey returns a random key from the database.
func (c *Client) RandomKey(ctx context.Context) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RANDOMKEY")
	return cmd
}

// Touch alters the last access time of a key.
func (c *Client) Touch(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "TOUCH")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ObjectEncoding returns the encoding of the object stored at key.
func (c *Client) ObjectEncoding(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "OBJECT", "ENCODING", key)
	return cmd
}

// MemoryUsage returns memory usage in bytes for a key.
func (c *Client) MemoryUsage(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "MEMORY", "USAGE", key)
	return cmd
}

// Sort sorts elements in a list, set, or sorted set.
func (c *Client) Sort(ctx context.Context, key string, sort *SortOptions) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := []string{"SORT", key}
	if sort != nil {
		args = append(args, sort.args()...)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// --- List Commands ---

// LPush pushes elements to the left of the list.
func (c *Client) LPush(ctx context.Context, key string, values ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(values))
	args = append(args, "LPUSH", key)
	for _, v := range values {
		args = append(args, formatArg(v))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// RPush pushes elements to the right of the list.
func (c *Client) RPush(ctx context.Context, key string, values ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(values))
	args = append(args, "RPUSH", key)
	for _, v := range values {
		args = append(args, formatArg(v))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// LPop removes and returns the leftmost element.
func (c *Client) LPop(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LPOP", key)
	return cmd
}

// RPop removes and returns the rightmost element.
func (c *Client) RPop(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "RPOP", key)
	return cmd
}

// LLen returns the length of the list.
func (c *Client) LLen(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LLEN", key)
	return cmd
}

// LRange returns elements from start to stop (inclusive).
func (c *Client) LRange(ctx context.Context, key string, start, stop int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LRANGE", key, strconv.FormatInt(start, 10), strconv.FormatInt(stop, 10))
	return cmd
}

// LIndex returns the element at the given index.
func (c *Client) LIndex(ctx context.Context, key string, index int64) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LINDEX", key, strconv.FormatInt(index, 10))
	return cmd
}

// LSet sets the element at the given index.
func (c *Client) LSet(ctx context.Context, key string, index int64, value interface{}) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LSET", key, strconv.FormatInt(index, 10), formatArg(value))
	return cmd
}

// LRem removes the first count occurrences of element.
func (c *Client) LRem(ctx context.Context, key string, count int64, value interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LREM", key, strconv.FormatInt(count, 10), formatArg(value))
	return cmd
}

// LInsertBefore inserts element before pivot.
func (c *Client) LInsertBefore(ctx context.Context, key string, pivot, element interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LINSERT", key, "BEFORE", formatArg(pivot), formatArg(element))
	return cmd
}

// LInsertAfter inserts element after pivot.
func (c *Client) LInsertAfter(ctx context.Context, key string, pivot, element interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LINSERT", key, "AFTER", formatArg(pivot), formatArg(element))
	return cmd
}

// LPushX pushes elements only if the list exists.
func (c *Client) LPushX(ctx context.Context, key string, values ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(values))
	args = append(args, "LPUSHX", key)
	for _, v := range values {
		args = append(args, formatArg(v))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// RPushX pushes elements only if the list exists.
func (c *Client) RPushX(ctx context.Context, key string, values ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(values))
	args = append(args, "RPUSHX", key)
	for _, v := range values {
		args = append(args, formatArg(v))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// BLPop is a blocking left pop. Timeout of 0 blocks indefinitely.
func (c *Client) BLPop(ctx context.Context, timeout time.Duration, keys ...string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := make([]string, 0, 2+len(keys))
	args = append(args, "BLPOP")
	args = append(args, keys...)
	args = append(args, formatTimeoutSeconds(timeout))
	val, err := c.processBlocking(ctx, timeout, args...)
	cmd.SetVal(val)
	cmd.SetErr(err)
	return cmd
}

// BRPop is a blocking right pop. Timeout of 0 blocks indefinitely.
func (c *Client) BRPop(ctx context.Context, timeout time.Duration, keys ...string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := make([]string, 0, 2+len(keys))
	args = append(args, "BRPOP")
	args = append(args, keys...)
	args = append(args, formatTimeoutSeconds(timeout))
	val, err := c.processBlocking(ctx, timeout, args...)
	cmd.SetVal(val)
	cmd.SetErr(err)
	return cmd
}

// --- Set Commands ---

// SAdd adds members to a set.
func (c *Client) SAdd(ctx context.Context, key string, members ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(members))
	args = append(args, "SADD", key)
	for _, m := range members {
		args = append(args, formatArg(m))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SRem removes members from a set.
func (c *Client) SRem(ctx context.Context, key string, members ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(members))
	args = append(args, "SREM", key)
	for _, m := range members {
		args = append(args, formatArg(m))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SMembers returns all members of the set.
func (c *Client) SMembers(ctx context.Context, key string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SMEMBERS", key)
	return cmd
}

// SIsMember reports whether member is in the set.
func (c *Client) SIsMember(ctx context.Context, key string, member interface{}) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SISMEMBER", key, formatArg(member))
	return cmd
}

// SMIsMember returns membership for multiple members.
func (c *Client) SMIsMember(ctx context.Context, key string, members ...interface{}) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := make([]string, 0, 2+len(members))
	args = append(args, "SMISMEMBER", key)
	for _, m := range members {
		args = append(args, formatArg(m))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SCard returns the number of members in the set.
func (c *Client) SCard(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SCARD", key)
	return cmd
}

// SPop removes and returns a random member.
func (c *Client) SPop(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SPOP", key)
	return cmd
}

// SPopN removes and returns up to count random members.
func (c *Client) SPopN(ctx context.Context, key string, count int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SPOP", key, strconv.FormatInt(count, 10))
	return cmd
}

// SRandMember returns a random member without removing it.
func (c *Client) SRandMember(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SRANDMEMBER", key)
	return cmd
}

// SRandMemberN returns up to count random members.
func (c *Client) SRandMemberN(ctx context.Context, key string, count int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SRANDMEMBER", key, strconv.FormatInt(count, 10))
	return cmd
}

// SUnion returns the union of all given sets.
func (c *Client) SUnion(ctx context.Context, keys ...string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "SUNION")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SUnionStore stores the union into destination.
func (c *Client) SUnionStore(ctx context.Context, destination string, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(keys))
	args = append(args, "SUNIONSTORE", destination)
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SInter returns the intersection of all given sets.
func (c *Client) SInter(ctx context.Context, keys ...string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "SINTER")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SInterStore stores the intersection into destination.
func (c *Client) SInterStore(ctx context.Context, destination string, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(keys))
	args = append(args, "SINTERSTORE", destination)
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SDiff returns the difference of the first set and all subsequent sets.
func (c *Client) SDiff(ctx context.Context, keys ...string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "SDIFF")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SDiffStore stores the difference into destination.
func (c *Client) SDiffStore(ctx context.Context, destination string, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(keys))
	args = append(args, "SDIFFSTORE", destination)
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SScan iterates set members with a cursor.
func (c *Client) SScan(ctx context.Context, key string, cursor uint64, match string, count int64) *ScanCmd {
	cmd := NewScanCmd(nil)
	args := []string{"SSCAN", key, strconv.FormatUint(cursor, 10)}
	if match != "" {
		args = append(args, "MATCH", match)
	}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseScanResult(cmd, val)
	return cmd
}

// --- Hash Commands ---

// HSet sets one or more hash fields.
func (c *Client) HSet(ctx context.Context, key string, values ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(values))
	args = append(args, "HSET", key)
	for _, v := range values {
		args = append(args, formatArg(v))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// HGet returns the value of a hash field.
func (c *Client) HGet(ctx context.Context, key, field string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HGET", key, field)
	return cmd
}

// HDel deletes one or more hash fields.
func (c *Client) HDel(ctx context.Context, key string, fields ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(fields))
	args = append(args, "HDEL", key)
	args = append(args, fields...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// HExists reports whether a hash field exists.
func (c *Client) HExists(ctx context.Context, key, field string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HEXISTS", key, field)
	return cmd
}

// HLen returns the number of fields in the hash.
func (c *Client) HLen(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HLEN", key)
	return cmd
}

// HKeys returns all field names in the hash.
func (c *Client) HKeys(ctx context.Context, key string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HKEYS", key)
	return cmd
}

// HVals returns all field values in the hash.
func (c *Client) HVals(ctx context.Context, key string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HVALS", key)
	return cmd
}

// HGetAll returns all fields and values of the hash.
func (c *Client) HGetAll(ctx context.Context, key string) *MapStringStringCmd {
	cmd := NewMapStringStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HGETALL", key)
	return cmd
}

// HSetNX sets a hash field only if it does not exist.
func (c *Client) HSetNX(ctx context.Context, key, field string, value interface{}) *BoolCmd {
	cmd := NewBoolCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HSETNX", key, field, formatArg(value))
	return cmd
}

// HIncrBy increments the integer value of a hash field.
func (c *Client) HIncrBy(ctx context.Context, key, field string, incr int64) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HINCRBY", key, field, strconv.FormatInt(incr, 10))
	return cmd
}

// HIncrByFloat increments the float value of a hash field.
func (c *Client) HIncrByFloat(ctx context.Context, key, field string, incr float64) *FloatCmd {
	cmd := NewFloatCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HINCRBYFLOAT", key, field, argFloat(incr))
	return cmd
}

// HMGet returns the values of multiple hash fields.
func (c *Client) HMGet(ctx context.Context, key string, fields ...string) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := make([]string, 0, 2+len(fields))
	args = append(args, "HMGET", key)
	args = append(args, fields...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// HScan iterates hash fields with a cursor.
func (c *Client) HScan(ctx context.Context, key string, cursor uint64, match string, count int64) *ScanCmd {
	cmd := NewScanCmd(nil)
	args := []string{"HSCAN", key, strconv.FormatUint(cursor, 10)}
	if match != "" {
		args = append(args, "MATCH", match)
	}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseScanResult(cmd, val)
	return cmd
}

// HRandField returns one random field from the hash, or "" and Nil if it does not exist.
func (c *Client) HRandField(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HRANDFIELD", key)
	return cmd
}

// HRandFieldN returns up to |count| distinct random fields (a negative count
// allows repeats and always returns exactly -count fields).
func (c *Client) HRandFieldN(ctx context.Context, key string, count int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HRANDFIELD", key, strconv.FormatInt(count, 10))
	return cmd
}

// HRandFieldWithValues returns up to |count| distinct random fields with their values.
func (c *Client) HRandFieldWithValues(ctx context.Context, key string, count int64) *KeyValueSliceCmd {
	cmd := NewKeyValueSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "HRANDFIELD", key, strconv.FormatInt(count, 10), "WITHVALUES")
	return cmd
}

// --- Sorted Set Commands ---

// ZAddArgs represents ZADD arguments.
type ZAddArgs struct {
	NX bool
	XX bool
	GT bool
	LT bool
	CH bool
}

// Z represents a scored member in a sorted set.
type Z struct {
	Score  float64
	Member string
}

// ZAdd adds members with scores to a sorted set.
func (c *Client) ZAdd(ctx context.Context, key string, members ...Z) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(members)*2)
	args = append(args, "ZADD", key)
	for _, m := range members {
		args = append(args, argScore(m.Score), m.Member)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZAddArgs adds members with optional flags (NX, XX, GT, LT, CH).
func (c *Client) ZAddArgs(ctx context.Context, key string, addArgs ZAddArgs, members ...Z) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 6+len(members)*2)
	args = append(args, "ZADD", key)
	if addArgs.NX {
		args = append(args, "NX")
	}
	if addArgs.XX {
		args = append(args, "XX")
	}
	if addArgs.GT {
		args = append(args, "GT")
	}
	if addArgs.LT {
		args = append(args, "LT")
	}
	if addArgs.CH {
		args = append(args, "CH")
	}
	for _, m := range members {
		args = append(args, argScore(m.Score), m.Member)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZRem removes members from the sorted set.
func (c *Client) ZRem(ctx context.Context, key string, members ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(members))
	args = append(args, "ZREM", key)
	for _, m := range members {
		args = append(args, formatArg(m))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZScore returns the score of a member.
func (c *Client) ZScore(ctx context.Context, key, member string) *FloatCmd {
	cmd := NewFloatCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZSCORE", key, member)
	return cmd
}

// ZRank returns the rank of a member (ascending).
func (c *Client) ZRank(ctx context.Context, key, member string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANK", key, member)
	return cmd
}

// ZRevRank returns the rank of a member (descending).
func (c *Client) ZRevRank(ctx context.Context, key, member string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZREVRANK", key, member)
	return cmd
}

// ZCard returns the number of members.
func (c *Client) ZCard(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZCARD", key)
	return cmd
}

// ZRange returns members by index range.
func (c *Client) ZRange(ctx context.Context, key string, start, stop int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANGE", key, strconv.FormatInt(start, 10), strconv.FormatInt(stop, 10))
	return cmd
}

// ZRangeWithScores returns members with scores by index range.
func (c *Client) ZRangeWithScores(ctx context.Context, key string, start, stop int64) *ZSliceCmd {
	cmd := NewZSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANGE", key, strconv.FormatInt(start, 10), strconv.FormatInt(stop, 10), "WITHSCORES")
	return cmd
}

// ZRevRange returns members by index range (descending).
func (c *Client) ZRevRange(ctx context.Context, key string, start, stop int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZREVRANGE", key, strconv.FormatInt(start, 10), strconv.FormatInt(stop, 10))
	return cmd
}

// ZRangeByScore returns members with scores in range.
func (c *Client) ZRangeByScore(ctx context.Context, key string, min, max string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANGEBYSCORE", key, min, max)
	return cmd
}

// ZRevRangeByScore returns members by score range (descending).
func (c *Client) ZRevRangeByScore(ctx context.Context, key string, max, min string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZREVRANGEBYSCORE", key, max, min)
	return cmd
}

// ZCount returns the count of members with scores in range.
func (c *Client) ZCount(ctx context.Context, key, min, max string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZCOUNT", key, min, max)
	return cmd
}

// ZIncrBy increments the score of a member.
func (c *Client) ZIncrBy(ctx context.Context, key string, increment float64, member string) *FloatCmd {
	cmd := NewFloatCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZINCRBY", key, argScore(increment), member)
	return cmd
}

// ZPopMin removes and returns members with lowest scores.
func (c *Client) ZPopMin(ctx context.Context, key string, count ...int64) *ZSliceCmd {
	cmd := NewZSliceCmd(nil)
	args := []string{"ZPOPMIN", key}
	if len(count) > 0 {
		args = append(args, strconv.FormatInt(count[0], 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZPopMax removes and returns members with highest scores.
func (c *Client) ZPopMax(ctx context.Context, key string, count ...int64) *ZSliceCmd {
	cmd := NewZSliceCmd(nil)
	args := []string{"ZPOPMAX", key}
	if len(count) > 0 {
		args = append(args, strconv.FormatInt(count[0], 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZUnionStore stores the union of sorted sets into destination.
func (c *Client) ZUnionStore(ctx context.Context, dest string, store *ZStore) *IntCmd {
	cmd := NewIntCmd(nil)
	args := store.args("ZUNIONSTORE", dest)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZInterStore stores the intersection of sorted sets into destination.
func (c *Client) ZInterStore(ctx context.Context, dest string, store *ZStore) *IntCmd {
	cmd := NewIntCmd(nil)
	args := store.args("ZINTERSTORE", dest)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// ZRandMember returns one random member, or "" and Nil if the key does not exist.
func (c *Client) ZRandMember(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANDMEMBER", key)
	return cmd
}

// ZRandMemberN returns up to |count| distinct random members (a negative
// count allows repeats and always returns exactly -count members).
func (c *Client) ZRandMemberN(ctx context.Context, key string, count int64) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANDMEMBER", key, strconv.FormatInt(count, 10))
	return cmd
}

// ZRandMemberWithScores returns up to |count| distinct random members with their scores.
func (c *Client) ZRandMemberWithScores(ctx context.Context, key string, count int64) *ZSliceCmd {
	cmd := NewZSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ZRANDMEMBER", key, strconv.FormatInt(count, 10), "WITHSCORES")
	return cmd
}

// ZScan iterates sorted set members with a cursor.
func (c *Client) ZScan(ctx context.Context, key string, cursor uint64, match string, count int64) *ScanCmd {
	cmd := NewScanCmd(nil)
	args := []string{"ZSCAN", key, strconv.FormatUint(cursor, 10)}
	if match != "" {
		args = append(args, "MATCH", match)
	}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	val, err := c.process(ctx, args...)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(val)
	parseScanResult(cmd, val)
	return cmd
}

// --- Stream Commands ---

// XAdd appends an entry to a stream, returning the assigned entry ID.
// Pass id "*" to let the server assign the ID.
func (c *Client) XAdd(ctx context.Context, key, id string, fields ...interface{}) *StringCmd {
	cmd := NewStringCmd(nil)
	args := make([]string, 0, 3+len(fields))
	args = append(args, "XADD", key, id)
	for _, v := range fields {
		args = append(args, formatArg(v))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// XLen returns the number of entries in a stream.
func (c *Client) XLen(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "XLEN", key)
	return cmd
}

// XRange returns entries between start and end (inclusive), oldest first.
func (c *Client) XRange(ctx context.Context, key, start, end string, count int64) *StreamEntrySliceCmd {
	cmd := NewStreamEntrySliceCmd(nil)
	args := []string{"XRANGE", key, start, end}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// XRevRange returns entries between end and start (inclusive), newest first.
func (c *Client) XRevRange(ctx context.Context, key, end, start string, count int64) *StreamEntrySliceCmd {
	cmd := NewStreamEntrySliceCmd(nil)
	args := []string{"XREVRANGE", key, end, start}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// XDel removes entries from a stream by ID.
func (c *Client) XDel(ctx context.Context, key string, ids ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(ids))
	args = append(args, "XDEL", key)
	args = append(args, ids...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// XTrimMaxLen trims a stream to (approximately, unless exact is true) maxLen entries.
func (c *Client) XTrimMaxLen(ctx context.Context, key string, maxLen int64, exact bool) *IntCmd {
	cmd := NewIntCmd(nil)
	approx := "~"
	if exact {
		approx = "="
	}
	c.processCmd(ctx, &cmd.Cmd, "XTRIM", key, "MAXLEN", approx, strconv.FormatInt(maxLen, 10))
	return cmd
}

// XRead reads entries appended after the given IDs from one or more streams.
// Use streams["key"] = "$" to read only new entries from now on.
func (c *Client) XRead(ctx context.Context, count int64, streams map[string]string) *XStreamSliceCmd {
	cmd := NewXStreamSliceCmd(nil)
	args := []string{"XREAD"}
	if count > 0 {
		args = append(args, "COUNT", strconv.FormatInt(count, 10))
	}
	args = append(args, "STREAMS")
	names := make([]string, 0, len(streams))
	ids := make([]string, 0, len(streams))
	for name, id := range streams {
		names = append(names, name)
		ids = append(ids, id)
	}
	args = append(args, names...)
	args = append(args, ids...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// XInfoStream returns metadata about a stream (length, last-generated-id,
// first/last entry, etc.), keyed exactly as the server names each field.
func (c *Client) XInfoStream(ctx context.Context, key string) *MapStringValueCmd {
	cmd := NewMapStringValueCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "XINFO", "STREAM", key)
	return cmd
}

// --- Bitmap Commands ---

// SetBit sets or clears the bit at offset.
func (c *Client) SetBit(ctx context.Context, key string, offset int64, value int) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SETBIT", key, strconv.FormatInt(offset, 10), strconv.Itoa(value))
	return cmd
}

// GetBit returns the bit value at offset.
func (c *Client) GetBit(ctx context.Context, key string, offset int64) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "GETBIT", key, strconv.FormatInt(offset, 10))
	return cmd
}

// BitCount counts set bits in a string.
func (c *Client) BitCount(ctx context.Context, key string, bitCount *BitCount) *IntCmd {
	cmd := NewIntCmd(nil)
	args := []string{"BITCOUNT", key}
	if bitCount != nil {
		args = append(args, strconv.FormatInt(bitCount.Start, 10), strconv.FormatInt(bitCount.End, 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// BitPos returns the position of the first bit set to value.
func (c *Client) BitPos(ctx context.Context, key string, bit int64, pos ...int64) *IntCmd {
	cmd := NewIntCmd(nil)
	args := []string{"BITPOS", key, strconv.FormatInt(bit, 10)}
	for _, p := range pos {
		args = append(args, strconv.FormatInt(p, 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// BitOpAnd performs bitwise AND.
func (c *Client) BitOpAnd(ctx context.Context, destKey string, keys ...string) *IntCmd {
	return c.bitOp(ctx, "AND", destKey, keys...)
}

// BitOpOr performs bitwise OR.
func (c *Client) BitOpOr(ctx context.Context, destKey string, keys ...string) *IntCmd {
	return c.bitOp(ctx, "OR", destKey, keys...)
}

// BitOpXor performs bitwise XOR.
func (c *Client) BitOpXor(ctx context.Context, destKey string, keys ...string) *IntCmd {
	return c.bitOp(ctx, "XOR", destKey, keys...)
}

// BitOpNot performs bitwise NOT.
func (c *Client) BitOpNot(ctx context.Context, destKey, key string) *IntCmd {
	return c.bitOp(ctx, "NOT", destKey, key)
}

func (c *Client) bitOp(ctx context.Context, op, destKey string, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 3+len(keys))
	args = append(args, "BITOP", op, destKey)
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// --- HyperLogLog Commands ---

// PFAdd adds elements to a HyperLogLog.
func (c *Client) PFAdd(ctx context.Context, key string, els ...interface{}) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 2+len(els))
	args = append(args, "PFADD", key)
	for _, el := range els {
		args = append(args, formatArg(el))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PFCount returns the approximated cardinality.
func (c *Client) PFCount(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := make([]string, 0, 1+len(keys))
	args = append(args, "PFCOUNT")
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PFMerge merges multiple HyperLogLogs into one.
func (c *Client) PFMerge(ctx context.Context, dest string, keys ...string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	args := make([]string, 0, 2+len(keys))
	args = append(args, "PFMERGE", dest)
	args = append(args, keys...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// --- Pub/Sub Commands ---

// Publish publishes a message to a channel.
func (c *Client) Publish(ctx context.Context, channel, message string) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PUBLISH", channel, message)
	return cmd
}

// PubSubChannels returns active channels matching pattern.
func (c *Client) PubSubChannels(ctx context.Context, pattern string) *StringSliceCmd {
	cmd := NewStringSliceCmd(nil)
	args := []string{"PUBSUB", "CHANNELS"}
	if pattern != "" {
		args = append(args, pattern)
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PubSubNumSub returns subscriber counts for channels.
func (c *Client) PubSubNumSub(ctx context.Context, channels ...string) *MapStringStringCmd {
	cmd := NewMapStringStringCmd(nil)
	args := make([]string, 0, 2+len(channels))
	args = append(args, "PUBSUB", "NUMSUB")
	args = append(args, channels...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// PubSubNumPat returns the count of pattern subscriptions.
func (c *Client) PubSubNumPat(ctx context.Context) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PUBSUB", "NUMPAT")
	return cmd
}

// --- Transaction Commands ---

// Watch watches keys for optimistic locking. The fn receives a Tx.
func (c *Client) Watch(ctx context.Context, fn func(*Tx) error, keys ...string) error {
	if c.cluster != nil {
		return c.cluster.watch(ctx, fn, keys...)
	}
	cn, err := c.getConn(ctx)
	if err != nil {
		return err
	}
	// The Tx runs every command, and its MULTI/EXEC pipeline, on this one
	// connection: WATCH state lives on the connection, so a pipeline that
	// took a different pooled connection would silently watch nothing.
	tx := &Tx{Client: &Client{opts: c.opts, fixed: cn}, conn: cn}
	broken := false
	defer func() {
		if broken {
			c.dropConn(cn)
			return
		}
		// Drop any watch still active so the connection goes back to the
		// pool clean (a no-op after EXEC or DISCARD).
		if _, err := cn.Do(ctx, "UNWATCH"); err != nil {
			c.dropConn(cn)
			return
		}
		c.putConn(cn)
	}()

	if len(keys) > 0 {
		args := make([]string, 0, 1+len(keys))
		args = append(args, "WATCH")
		args = append(args, keys...)
		val, err := cn.Do(ctx, args...)
		if err != nil {
			broken = true
			return err
		}
		if val.IsErr() {
			return NewError(val.Str)
		}
	}

	return fn(tx)
}

// --- Server Commands ---

// Ping tests connectivity.
func (c *Client) Ping(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "PING")
	return cmd
}

// Echo echoes the given message.
func (c *Client) Echo(ctx context.Context, message string) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "ECHO", message)
	return cmd
}

// Info returns server information.
func (c *Client) Info(ctx context.Context, sections ...string) *StringCmd {
	cmd := NewStringCmd(nil)
	args := make([]string, 0, 1+len(sections))
	args = append(args, "INFO")
	args = append(args, sections...)
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// DBSize returns the number of keys in the current database.
func (c *Client) DBSize(ctx context.Context) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "DBSIZE")
	return cmd
}

// FlushDB deletes all keys in the current database.
func (c *Client) FlushDB(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "FLUSHDB")
	return cmd
}

// FlushAll deletes all keys in all databases.
func (c *Client) FlushAll(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "FLUSHALL")
	return cmd
}

// Save synchronously saves all databases to disk.
func (c *Client) Save(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SAVE")
	return cmd
}

// BgSave asynchronously saves all databases to disk.
func (c *Client) BgSave(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "BGSAVE")
	return cmd
}

// BgRewriteAOF asynchronously rewrites the AOF.
func (c *Client) BgRewriteAOF(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "BGREWRITEAOF")
	return cmd
}

// LastSave returns the UNIX timestamp of the last successful save.
func (c *Client) LastSave(ctx context.Context) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "LASTSAVE")
	return cmd
}

// Time returns the server time as [seconds, microseconds].
func (c *Client) Time(ctx context.Context) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "TIME")
	return cmd
}

// ConfigGet returns configuration parameters matching pattern.
func (c *Client) ConfigGet(ctx context.Context, parameter string) *MapStringStringCmd {
	cmd := NewMapStringStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CONFIG", "GET", parameter)
	return cmd
}

// ConfigSet sets a configuration parameter.
func (c *Client) ConfigSet(ctx context.Context, parameter, value string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CONFIG", "SET", parameter, value)
	return cmd
}

// ConfigResetStat resets server statistics.
func (c *Client) ConfigResetStat(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CONFIG", "RESETSTAT")
	return cmd
}

// Command returns the total number of commands.
func (c *Client) Command(ctx context.Context) *SliceCmd {
	cmd := NewSliceCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "COMMAND")
	return cmd
}

// ClientSetName sets the connection name.
func (c *Client) ClientSetName(ctx context.Context, name string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CLIENT", "SETNAME", name)
	return cmd
}

// ClientGetName returns the connection name.
func (c *Client) ClientGetName(ctx context.Context) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CLIENT", "GETNAME")
	return cmd
}

// ClientID returns the current connection's ID.
func (c *Client) ClientID(ctx context.Context) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CLIENT", "ID")
	return cmd
}

// ClientList returns the list of client connections.
func (c *Client) ClientList(ctx context.Context) *StringCmd {
	cmd := NewStringCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "CLIENT", "LIST")
	return cmd
}

// Select selects the database.
func (c *Client) Select(ctx context.Context, index int) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SELECT", strconv.Itoa(index))
	return cmd
}

// SwapDB swaps two databases atomically.
func (c *Client) SwapDB(ctx context.Context, index1, index2 int) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SWAPDB", strconv.Itoa(index1), strconv.Itoa(index2))
	return cmd
}

// Shutdown shuts down the server, saving the snapshot when it has save
// points. Its result is "OK" once the server has closed the connection.
func (c *Client) Shutdown(ctx context.Context) *StatusCmd {
	return c.shutdown(ctx, "SHUTDOWN")
}

// ShutdownSave shuts down with save.
func (c *Client) ShutdownSave(ctx context.Context) *StatusCmd {
	return c.shutdown(ctx, "SHUTDOWN", "SAVE")
}

// ShutdownNoSave shuts down without saving.
func (c *Client) ShutdownNoSave(ctx context.Context) *StatusCmd {
	return c.shutdown(ctx, "SHUTDOWN", "NOSAVE")
}

// shutdown sends SHUTDOWN once, never retried. The server closes the
// connection without a reply as it shuts down (from 0.1.54):
// that is success. An error reply says why it did not shut down; the OK of
// earlier servers is success too.
func (c *Client) shutdown(ctx context.Context, args ...string) *StatusCmd {
	cmd := NewStatusCmd(nil)
	if c.cluster != nil {
		cmd.SetErr(errors.New("hotkv: SHUTDOWN on a cluster client: connect a Client to the node you want to stop"))
		return cmd
	}
	cn, err := c.getConn(ctx)
	if err != nil {
		cmd.SetErr(err)
		return cmd
	}
	val, err := cn.Do(ctx, args...)
	if err != nil {
		c.dropConn(cn)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || isRetryableError(err) {
			cmd.SetVal(Value{Typ: RespSimpleString, Str: "OK"})
			return cmd
		}
		cmd.SetErr(err)
		return cmd
	}
	c.putConn(cn)
	if val.IsErr() {
		cmd.SetErr(NewError(val.Str))
		return cmd
	}
	cmd.SetVal(val)
	return cmd
}

// SlowLogGet returns slow log entries.
func (c *Client) SlowLogGet(ctx context.Context, count int64) *SliceCmd {
	cmd := NewSliceCmd(nil)
	args := []string{"SLOWLOG", "GET"}
	if count >= 0 {
		args = append(args, strconv.FormatInt(count, 10))
	}
	c.processCmd(ctx, &cmd.Cmd, args...)
	return cmd
}

// SlowLogLen returns the number of slow log entries.
func (c *Client) SlowLogLen(ctx context.Context) *IntCmd {
	cmd := NewIntCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SLOWLOG", "LEN")
	return cmd
}

// SlowLogReset resets the slow log.
func (c *Client) SlowLogReset(ctx context.Context) *StatusCmd {
	cmd := NewStatusCmd(nil)
	c.processCmd(ctx, &cmd.Cmd, "SLOWLOG", "RESET")
	return cmd
}

// --- Helpers ---

// SortOptions configure the SORT command.
type SortOptions struct {
	Offset int64
	Count  int64
	Asc    bool
	Desc   bool
	Alpha  bool
}

func (s *SortOptions) args() []string {
	var args []string
	if s.Offset != 0 || s.Count != 0 {
		args = append(args, "LIMIT", strconv.FormatInt(s.Offset, 10), strconv.FormatInt(s.Count, 10))
	}
	if s.Asc {
		args = append(args, "ASC")
	}
	if s.Desc {
		args = append(args, "DESC")
	}
	if s.Alpha {
		args = append(args, "ALPHA")
	}
	return args
}

// ZStore configures ZUNIONSTORE/ZINTERSTORE.
type ZStore struct {
	Keys      []string
	Weights   []float64
	Aggregate string // SUM, MIN, MAX
}

func (z *ZStore) args(cmd, dest string) []string {
	args := make([]string, 0, 4+len(z.Keys)+len(z.Weights)*2)
	args = append(args, cmd, dest, strconv.Itoa(len(z.Keys)))
	args = append(args, z.Keys...)
	if len(z.Weights) > 0 {
		args = append(args, "WEIGHTS")
		for _, w := range z.Weights {
			args = append(args, argScore(w))
		}
	}
	if z.Aggregate != "" {
		args = append(args, "AGGREGATE", z.Aggregate)
	}
	return args
}

// BitCount represents the range for BITCOUNT.
type BitCount struct {
	Start int64
	End   int64
}

func usePX(d time.Duration) bool {
	return d > 0 && d < time.Second || d%time.Second != 0
}

func parseScanResult(cmd *ScanCmd, val Value) {
	if val.Typ != RespArray || len(val.Elems) != 2 {
		return
	}
	cursor, err := strconv.ParseUint(val.Elems[0].Str, 10, 64)
	if err != nil {
		return
	}
	var keys []string
	for _, e := range val.Elems[1].Elems {
		keys = append(keys, e.Str)
	}
	cmd.SetScanResult(cursor, keys)
}

// Tx represents a transaction created by Watch. It embeds a Client pinned
// to the watched connection, so every typed command method (tx.Get, tx.HGet,
// ...) and Do run on that connection, between WATCH and EXEC.
type Tx struct {
	*Client
	conn *Conn
}

// Pipeline returns a pipelined transaction (MULTI/EXEC) on the watched
// connection.
func (tx *Tx) Pipeline() *Pipeline {
	return &Pipeline{
		client: tx.Client,
		tx:     true,
	}
}

// TxPipeline is the same as Pipeline.
func (tx *Tx) TxPipeline() *Pipeline { return tx.Pipeline() }
