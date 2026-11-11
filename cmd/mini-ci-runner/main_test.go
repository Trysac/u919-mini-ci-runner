package main

import (
	"bytes"
	"testing"
)

func TestMainRunUsageError(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := Run(nil, &out, &errBuf)
	if code != 2 {
		t.Fatalf("Run(nil) = %d, want 2", code)
	}
}
