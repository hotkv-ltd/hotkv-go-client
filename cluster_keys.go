// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"strconv"
	"strings"
)

// This file extracts the keys of a command so the cluster client can find
// its hash slot. The server describes every command's key positions in the
// reply of COMMAND ("key specifications"): where the keys begin
// (a fixed index, or after a keyword such as STREAMS) and how many follow
// (a range, or a count argument such as numkeys). The cluster client loads
// that table once from a node and applies it, so no per-command knowledge
// is hard coded in the SDK and new server commands route correctly without
// an SDK release.

// keySpec is one entry of a command's key specifications.
type keySpec struct {
	// begin_search
	bsType    string // "index" or "keyword"; anything else is unusable
	bsIndex   int
	bsKeyword string
	bsStartAt int
	// find_keys
	fkType     string // "range" or "keynum"
	fkLastKey  int
	fkKeyStep  int
	fkLimit    int
	fkKeyNum   int // keynumidx
	fkFirstKey int
}

// cmdInfo is the routing-relevant part of one COMMAND entry.
type cmdInfo struct {
	name     string
	arity    int
	readOnly bool
	movable  bool
	first    int
	last     int
	step     int
	specs    []keySpec
	subs     map[string]*cmdInfo
}

// cmdTable maps lower-case command names to their routing info.
type cmdTable map[string]*cmdInfo

// parseCommandTable decodes the reply of COMMAND.
func parseCommandTable(v Value) cmdTable {
	t := make(cmdTable, len(v.Elems))
	for _, e := range v.Elems {
		ci := parseCmdInfo(e)
		if ci != nil {
			t[ci.name] = ci
		}
	}
	return t
}

func parseCmdInfo(e Value) *cmdInfo {
	if e.Typ != RespArray && e.Typ != RespSet {
		return nil
	}
	if len(e.Elems) < 6 {
		return nil
	}
	ci := &cmdInfo{
		name:  strings.ToLower(e.Elems[0].Str),
		arity: int(valInt(e.Elems[1])),
		first: int(valInt(e.Elems[3])),
		last:  int(valInt(e.Elems[4])),
		step:  int(valInt(e.Elems[5])),
	}
	for _, f := range e.Elems[2].Elems {
		switch strings.ToLower(f.Str) {
		case "readonly":
			ci.readOnly = true
		case "movablekeys":
			ci.movable = true
		}
	}
	// Elements 6 (ACL categories) and 7 (tips) are not needed. Element 8
	// holds the key specifications, element 9 the subcommands.
	if len(e.Elems) > 8 {
		for _, ks := range e.Elems[8].Elems {
			if spec, ok := parseKeySpec(ks); ok {
				ci.specs = append(ci.specs, spec)
			} else {
				// An unparseable spec makes the table unreliable for this
				// command: fall back to the legacy first/last/step triple.
				ci.specs = nil
				break
			}
		}
	}
	if len(e.Elems) > 9 && len(e.Elems[9].Elems) > 0 {
		ci.subs = make(map[string]*cmdInfo, len(e.Elems[9].Elems))
		for _, se := range e.Elems[9].Elems {
			if sub := parseCmdInfo(se); sub != nil {
				// Subcommands are named "parent|sub".
				name := sub.name
				if i := strings.IndexByte(name, '|'); i >= 0 {
					name = name[i+1:]
				}
				ci.subs[name] = sub
			}
		}
	}
	return ci
}

// asStringMap turns a RESP3 map or a RESP2 flat key/value array into a map.
func asStringMap(v Value) map[string]Value {
	m := map[string]Value{}
	switch v.Typ {
	case RespMap:
		for _, kv := range v.MapVal {
			m[strings.ToLower(kv.Key.Str)] = kv.Val
		}
	case RespArray, RespSet:
		for i := 0; i+1 < len(v.Elems); i += 2 {
			m[strings.ToLower(v.Elems[i].Str)] = v.Elems[i+1]
		}
	}
	return m
}

func parseKeySpec(v Value) (keySpec, bool) {
	var ks keySpec
	top := asStringMap(v)
	bs, ok := top["begin_search"]
	if !ok {
		return ks, false
	}
	fk, ok := top["find_keys"]
	if !ok {
		return ks, false
	}
	bsm := asStringMap(bs)
	ks.bsType = strings.ToLower(bsm["type"].Str)
	bsSpec := asStringMap(bsm["spec"])
	switch ks.bsType {
	case "index":
		ks.bsIndex = int(valInt(bsSpec["index"]))
	case "keyword":
		ks.bsKeyword = bsSpec["keyword"].Str
		ks.bsStartAt = int(valInt(bsSpec["startfrom"]))
	default:
		return ks, false
	}
	fkm := asStringMap(fk)
	ks.fkType = strings.ToLower(fkm["type"].Str)
	fkSpec := asStringMap(fkm["spec"])
	switch ks.fkType {
	case "range":
		ks.fkLastKey = int(valInt(fkSpec["lastkey"]))
		ks.fkKeyStep = int(valInt(fkSpec["keystep"]))
		ks.fkLimit = int(valInt(fkSpec["limit"]))
	case "keynum":
		ks.fkKeyNum = int(valInt(fkSpec["keynumidx"]))
		ks.fkFirstKey = int(valInt(fkSpec["firstkey"]))
		ks.fkKeyStep = int(valInt(fkSpec["keystep"]))
	default:
		return ks, false
	}
	if ks.fkKeyStep <= 0 {
		ks.fkKeyStep = 1
	}
	return ks, true
}

// lookup finds the info for a command line, descending into subcommands
// (OBJECT ENCODING, XINFO STREAM, ...).
func (t cmdTable) lookup(args []string) *cmdInfo {
	if len(args) == 0 {
		return nil
	}
	ci := t[strings.ToLower(args[0])]
	if ci == nil {
		return nil
	}
	if len(ci.subs) > 0 && len(args) > 1 {
		if sub := ci.subs[strings.ToLower(args[1])]; sub != nil {
			return sub
		}
	}
	return ci
}

// keyIndexes returns the argument positions that hold keys. ok is false
// when the command's key positions are unknown.
func (ci *cmdInfo) keyIndexes(args []string) []int {
	argc := len(args)
	var idx []int
	if len(ci.specs) > 0 {
		for _, ks := range ci.specs {
			idx = ks.appendIndexes(idx, args)
		}
		return idx
	}
	// Legacy triple: first, last, step. first == 0 means "no keys".
	if ci.first <= 0 {
		return nil
	}
	last := ci.last
	if last < 0 {
		last = argc + last
	}
	step := ci.step
	if step <= 0 {
		step = 1
	}
	for i := ci.first; i <= last && i < argc; i += step {
		idx = append(idx, i)
	}
	return idx
}

func (ks keySpec) appendIndexes(dst []int, args []string) []int {
	argc := len(args)
	first := 0
	switch ks.bsType {
	case "index":
		first = ks.bsIndex
		if first < 0 {
			first += argc
		}
	case "keyword":
		start, end, delta := ks.bsStartAt, argc-1, 1
		if ks.bsStartAt < 0 {
			start = argc + ks.bsStartAt
			end = 1
			delta = -1
		}
		found := -1
		for i := start; i >= 1 && i < argc; i += delta {
			if strings.EqualFold(args[i], ks.bsKeyword) {
				found = i + 1
				break
			}
			if i == end {
				break
			}
		}
		if found < 0 {
			return dst
		}
		first = found
	default:
		return dst
	}
	if first < 1 || first >= argc {
		return dst
	}

	var last int
	switch ks.fkType {
	case "range":
		switch {
		case ks.fkLastKey >= 0:
			last = first + ks.fkLastKey
		case ks.fkLimit == 0:
			last = argc + ks.fkLastKey
		default:
			last = first + ((argc-first)/ks.fkLimit + ks.fkLastKey)
		}
	case "keynum":
		ni := first + ks.fkKeyNum
		if ni < 1 || ni >= argc {
			return dst
		}
		n, err := strconv.Atoi(args[ni])
		if err != nil || n <= 0 {
			return dst
		}
		first += ks.fkFirstKey
		last = first + n - 1
	default:
		return dst
	}
	for i := first; i <= last && i < argc; i += ks.fkKeyStep {
		dst = append(dst, i)
	}
	return dst
}
