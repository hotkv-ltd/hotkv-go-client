// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"context"
	"sync"
	"time"
)

// pipelineCmd represents a single command queued in a pipeline.
type pipelineCmd struct {
	args []string
	cmd  *Cmd
}

// Pipeline buffers commands and sends them all at once using RESP pipelining.
// Use Client.Pipeline() or Client.TxPipeline() to create one.
type Pipeline struct {
	client *Client
	mu     sync.Mutex
	cmds   []pipelineCmd
	tx     bool // whether to wrap in MULTI/EXEC
	closed bool
	argErr error // first argument-encoding error among the queued commands
}

func newPipeline(c *Client) *Pipeline {
	return &Pipeline{client: c}
}

func newTxPipeline(c *Client) *Pipeline {
	return &Pipeline{client: c, tx: true}
}

// queue adds a command to the pipeline, associating it with cmd (normally
// the embedded Cmd of a typed *StatusCmd/*StringCmd/etc. the caller already
// holds a pointer to) so Exec() populates that same object. Queuing a new,
// unshared *Cmd here instead (the way this used to work) would let Exec()
// fill in a copy nobody can see, leaving every typed pipeline result stuck
// at its zero value.
func (p *Pipeline) queue(cmd *Cmd, args ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := checkArgs(args); err != nil {
		// An argument that cannot be encoded (NaN, an unsupported type)
		// fails the command now; Exec reports it and sends nothing.
		cmd.SetErr(err)
		if p.argErr == nil {
			p.argErr = err
		}
	}
	p.cmds = append(p.cmds, pipelineCmd{args: args, cmd: cmd})
}

// Len returns the number of queued commands.
func (p *Pipeline) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cmds)
}

// Discard resets the pipeline, discarding all queued commands.
func (p *Pipeline) Discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cmds = p.cmds[:0]
}

// Close marks the pipeline as closed and discards queued commands.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cmds = nil
	return nil
}

// Exec sends all queued commands to the server and populates their results.
// For MULTI/EXEC pipelines, the commands are wrapped in MULTI ... EXEC.
func (p *Pipeline) Exec(ctx context.Context) ([]Cmd, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	cmds := make([]pipelineCmd, len(p.cmds))
	copy(cmds, p.cmds)
	p.cmds = p.cmds[:0]
	argErr := p.argErr
	p.argErr = nil
	p.mu.Unlock()

	if len(cmds) == 0 {
		return nil, nil
	}
	if argErr != nil {
		// Nothing is sent when any queued command is unsendable.
		for i := range cmds {
			if cmds[i].cmd.Err() == nil {
				cmds[i].cmd.SetErr(argErr)
			}
		}
		return collectResults(cmds), argErr
	}

	if p.client.cluster != nil {
		results, err := p.client.cluster.execPipeline(ctx, cmds, p.tx)
		return results, err
	}

	cn, err := p.client.getConn(ctx)
	if err != nil {
		for i := range cmds {
			cmds[i].cmd.SetErr(err)
		}
		return collectResults(cmds), err
	}

	cn.mu.Lock()
	cn.usedAt = now()

	var execErr error
	if p.tx {
		execErr = p.execTx(ctx, cn, cmds)
	} else {
		execErr = p.execPlain(ctx, cn, cmds)
	}
	cn.mu.Unlock()

	// Must run after cn.mu is released: Put() calls cn.IsClosed(), which
	// locks the same mutex, and Go's sync.Mutex is not reentrant.
	if execErr != nil {
		p.client.dropConn(cn)
	} else {
		p.client.putConn(cn)
	}

	return collectResults(cmds), execErr
}

func (p *Pipeline) execPlain(ctx context.Context, cn *Conn, cmds []pipelineCmd) error {
	// Write all commands.
	for _, c := range cmds {
		if err := cn.wr.WriteCommand(c.args...); err != nil {
			return err
		}
	}
	if err := cn.wr.Flush(); err != nil {
		return err
	}

	// Read all replies.
	for i := range cmds {
		val, err := cn.ReadValue(ctx)
		if err != nil {
			// Mark remaining commands as failed.
			for j := i; j < len(cmds); j++ {
				cmds[j].cmd.SetErr(err)
			}
			return err
		}
		if val.IsErr() {
			cmds[i].cmd.SetErr(NewError(val.Str))
		}
		cmds[i].cmd.SetVal(val)
	}
	return nil
}

func (p *Pipeline) execTx(ctx context.Context, cn *Conn, cmds []pipelineCmd) error {
	// MULTI
	if err := cn.wr.WriteCommand("MULTI"); err != nil {
		return err
	}

	// Queued commands.
	for _, c := range cmds {
		if err := cn.wr.WriteCommand(c.args...); err != nil {
			return err
		}
	}

	// EXEC
	if err := cn.wr.WriteCommand("EXEC"); err != nil {
		return err
	}
	if err := cn.wr.Flush(); err != nil {
		return err
	}

	// Read MULTI reply (+OK).
	multiVal, err := cn.ReadValue(ctx)
	if err != nil {
		return err
	}
	if multiVal.IsErr() {
		return NewError(multiVal.Str)
	}

	// Read QUEUED replies for each command.
	for i := range cmds {
		val, err := cn.ReadValue(ctx)
		if err != nil {
			return err
		}
		if val.IsErr() {
			cmds[i].cmd.SetErr(NewError(val.Str))
		}
	}

	// Read EXEC reply (array of results, or nil on WATCH failure).
	execVal, err := cn.ReadValue(ctx)
	if err != nil {
		return err
	}
	if execVal.IsNil() {
		err := TxFailedErr
		for i := range cmds {
			cmds[i].cmd.SetErr(err)
		}
		return err
	}
	if execVal.IsErr() {
		return NewError(execVal.Str)
	}

	// Populate results from EXEC array.
	for i := range cmds {
		if i < len(execVal.Elems) {
			v := execVal.Elems[i]
			if v.IsErr() {
				cmds[i].cmd.SetErr(NewError(v.Str))
			}
			cmds[i].cmd.SetVal(v)
		}
	}
	return nil
}

func collectResults(cmds []pipelineCmd) []Cmd {
	results := make([]Cmd, len(cmds))
	for i, c := range cmds {
		results[i] = *c.cmd
	}
	return results
}

// --- Convenience command methods on Pipeline ---
// These mirror the Client methods but queue instead of executing immediately.

func (p *Pipeline) Set(ctx context.Context, key, value string, args ...string) *StatusCmd {
	cmdArgs := append([]string{"SET", key, value}, args...)
	cmd := NewStatusCmd(nil)
	p.queue(&cmd.Cmd, cmdArgs...)
	return cmd
}

func (p *Pipeline) Get(ctx context.Context, key string) *StringCmd {
	cmd := NewStringCmd(nil)
	p.queue(&cmd.Cmd, "GET", key)
	return cmd
}

func (p *Pipeline) Del(ctx context.Context, keys ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := append([]string{"DEL"}, keys...)
	p.queue(&cmd.Cmd, args...)
	return cmd
}

func (p *Pipeline) Incr(ctx context.Context, key string) *IntCmd {
	cmd := NewIntCmd(nil)
	p.queue(&cmd.Cmd, "INCR", key)
	return cmd
}

func (p *Pipeline) HSet(ctx context.Context, key string, fieldValues ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := append([]string{"HSET", key}, fieldValues...)
	p.queue(&cmd.Cmd, args...)
	return cmd
}

func (p *Pipeline) HGet(ctx context.Context, key, field string) *StringCmd {
	cmd := NewStringCmd(nil)
	p.queue(&cmd.Cmd, "HGET", key, field)
	return cmd
}

func (p *Pipeline) LPush(ctx context.Context, key string, values ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := append([]string{"LPUSH", key}, values...)
	p.queue(&cmd.Cmd, args...)
	return cmd
}

func (p *Pipeline) RPush(ctx context.Context, key string, values ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := append([]string{"RPUSH", key}, values...)
	p.queue(&cmd.Cmd, args...)
	return cmd
}

func (p *Pipeline) SAdd(ctx context.Context, key string, members ...string) *IntCmd {
	cmd := NewIntCmd(nil)
	args := append([]string{"SADD", key}, members...)
	p.queue(&cmd.Cmd, args...)
	return cmd
}

func (p *Pipeline) Expire(ctx context.Context, key string, seconds string) *BoolCmd {
	cmd := NewBoolCmd(nil)
	p.queue(&cmd.Cmd, "EXPIRE", key, seconds)
	return cmd
}

// Do queues a raw command with arbitrary arguments (same argument types as
// Client.Do). Its reply is available from the returned Cmd after Exec.
func (p *Pipeline) Do(ctx context.Context, args ...interface{}) *Cmd {
	cmd := &Cmd{}
	strs, err := convertArgs(args)
	if err != nil {
		cmd.SetErr(err)
		p.mu.Lock()
		if p.argErr == nil {
			p.argErr = err
		}
		p.cmds = append(p.cmds, pipelineCmd{args: []string{"PING"}, cmd: cmd})
		p.mu.Unlock()
		return cmd
	}
	p.queue(cmd, strs...)
	return cmd
}

// now returns the current time, broken out for testing.
var now = func() time.Time { return time.Now() }
