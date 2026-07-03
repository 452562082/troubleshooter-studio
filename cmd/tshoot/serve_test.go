package main

import (
	"testing"
	"time"
)

func TestParseServeFlagsDefaults(t *testing.T) {
	opts, err := parseServeFlags(nil)
	if err != nil {
		t.Fatalf("parseServeFlags(nil): %v", err)
	}
	if opts.addr != "127.0.0.1:8080" {
		t.Fatalf("addr = %q, want 127.0.0.1:8080", opts.addr)
	}
	if opts.readHeaderTimeout != 5*time.Second {
		t.Fatalf("readHeaderTimeout = %v, want 5s", opts.readHeaderTimeout)
	}
}

func TestParseServeFlagsCustomAddr(t *testing.T) {
	opts, err := parseServeFlags([]string{"--addr", "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("parseServeFlags custom addr: %v", err)
	}
	if opts.addr != "127.0.0.1:0" {
		t.Fatalf("addr = %q, want 127.0.0.1:0", opts.addr)
	}
}
