// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseURL parses a connection URL of the form
// "(hotkv|hotkvs|redis|rediss)://[user[:pass]@]host[:port][/db]" into
// Options. hotkvs:// and rediss:// enable TLS with a default *tls.Config;
// redis:// and rediss:// are accepted for drop-in migration and behave
// identically to hotkv:// and hotkvs://. Unset fields are left at their
// Options zero value, so the result should still be passed through the
// normal NewClient(opts) path (its defaults apply as usual).
func ParseURL(rawURL string) (*Options, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("hotkv: invalid URL: %w", err)
	}

	var useTLS bool
	switch strings.ToLower(u.Scheme) {
	case "hotkv", "redis":
		useTLS = false
	case "hotkvs", "rediss":
		useTLS = true
	default:
		return nil, fmt.Errorf("hotkv: unsupported URL scheme %q (want hotkv, hotkvs, redis, or rediss)", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("hotkv: invalid port in URL: %q", port)
	}

	opts := &Options{
		Addr: net.JoinHostPort(host, port),
	}

	if u.User != nil {
		if pass, ok := u.User.Password(); ok {
			opts.Password = pass
			if name := u.User.Username(); name != "" {
				opts.Username = name
			}
		} else if name := u.User.Username(); name != "" {
			// A single userinfo component with no colon is the legacy
			// single-password AUTH form, e.g. redis://:secret@host is the
			// canonical spelling, but accept redis://secret@host too.
			opts.Password = name
		}
	}

	if path := strings.TrimPrefix(u.Path, "/"); path != "" {
		db, err := strconv.Atoi(path)
		if err != nil {
			return nil, fmt.Errorf("hotkv: invalid database index in URL path: %q", path)
		}
		opts.DB = db
	}

	if useTLS {
		opts.TLSConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}

	return opts, nil
}
