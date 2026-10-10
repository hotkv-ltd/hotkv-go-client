// Copyright (c) 2026 HotKV Ltd
// Licensed under the Apache License, Version 2.0. See LICENSE in the project root.

package hotkv

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The publish workflow refuses a tag that disagrees with the Version constant
// or the go.mod module path; this keeps them in step before a tag exists.
func TestVersionMatchesModule(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(Version) {
		t.Fatalf("Version %q is not a semantic version", Version)
	}
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	want := "module github.com/hotkv-ltd/hotkv-go-client"
	if major := strings.SplitN(Version, ".", 2)[0]; major != "0" && major != "1" {
		want += "/v" + major
	}
	if first := strings.TrimSpace(strings.SplitN(string(mod), "\n", 2)[0]); first != want {
		t.Errorf("go.mod starts with %q, want %q", first, want)
	}
}
