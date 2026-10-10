// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import "strings"

// CRC16 computes the raw CRC16/XMODEM checksum of data (polynomial 0x1021,
// initial value 0, no reflection): the same variant HotKV cluster
// mode uses. CRC16([]byte("123456789")) == 0x31C3.
func CRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

const clusterSlots = 16384

// KeySlot computes the cluster hash slot (0..16383) for a key, honoring
// `{hash tag}` substrings exactly like CLUSTER KEYSLOT: if the key contains
// '{' and a '}' follows it with at least one byte in between, only the
// bytes between them are hashed; otherwise the whole key is hashed.
func KeySlot(key string) uint16 {
	hashed := key
	if open := strings.IndexByte(key, '{'); open >= 0 {
		if close := strings.IndexByte(key[open+1:], '}'); close > 0 {
			hashed = key[open+1 : open+1+close]
		}
	}
	return CRC16([]byte(hashed)) % clusterSlots
}
