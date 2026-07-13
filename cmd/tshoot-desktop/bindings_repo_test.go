package main

import (
	"context"
	"errors"
	"testing"
)

func TestCancelAnalyzeCancelsActiveAnalyzeContext(t *testing.T) {
	app := &App{}
	ctx, done := app.beginAnalyzeContext()
	if err := ctx.Err(); err != nil {
		t.Fatalf("new analyze context err = %v", err)
	}

	if !app.CancelAnalyze() {
		t.Fatal("CancelAnalyze should report an active analyze was canceled")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("analyze context err = %v, want context.Canceled", ctx.Err())
	}

	done()
	if app.CancelAnalyze() {
		t.Fatal("CancelAnalyze should report false after analyze context is done")
	}
}
