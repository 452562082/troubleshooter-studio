package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestServeHandlerServesSchema(t *testing.T) {
	server := httptest.NewServer(newServeHandler(t.TempDir()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/schema")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "schema_version") {
		t.Fatalf("schema response missing schema_version: %.200s", buf.String())
	}
}
