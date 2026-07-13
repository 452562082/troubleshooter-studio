package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWaitForVerifiedZentaoSessionKeepsPollingUntilVerified(t *testing.T) {
	oldRead := readZentaoCookies
	oldVerify := verifyCapturedZentaoSession
	oldPollInterval := zentaoLoginPollInterval
	t.Cleanup(func() {
		readZentaoCookies = oldRead
		verifyCapturedZentaoSession = oldVerify
		zentaoLoginPollInterval = oldPollInterval
	})
	zentaoLoginPollInterval = time.Millisecond
	reads := 0
	readZentaoCookies = func(port int, host string) ([]chromeCookie, error) {
		reads++
		if reads == 1 {
			return []chromeCookie{{Name: "zentaosid", Value: "anonymous", Domain: host}}, nil
		}
		return []chromeCookie{{Name: "zentaosid", Value: "logged-in", Domain: host}}, nil
	}
	verifyCapturedZentaoSession = func(baseURL string, sessionHeader string) error {
		if sessionHeader == "Cookie: zentaosid=anonymous" {
			return errors.New("login required")
		}
		if sessionHeader != "Cookie: zentaosid=logged-in" {
			t.Fatalf("sessionHeader = %q", sessionHeader)
		}
		return nil
	}
	u, err := url.Parse("http://zentao.example.com/")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header, count, err := waitForVerifiedZentaoSession(ctx, 12345, u)
	if err != nil {
		t.Fatalf("waitForVerifiedZentaoSession: %v", err)
	}
	if header != "Cookie: zentaosid=logged-in" || count != 1 || reads != 2 {
		t.Fatalf("header=%q count=%d reads=%d", header, count, reads)
	}
}

func TestWaitForVerifiedZentaoSessionStopsWhenControlledBrowserCloses(t *testing.T) {
	oldRead := readZentaoCookies
	oldVerify := verifyCapturedZentaoSession
	oldPollInterval := zentaoLoginPollInterval
	t.Cleanup(func() {
		readZentaoCookies = oldRead
		verifyCapturedZentaoSession = oldVerify
		zentaoLoginPollInterval = oldPollInterval
	})
	zentaoLoginPollInterval = time.Millisecond
	reads := 0
	readZentaoCookies = func(port int, host string) ([]chromeCookie, error) {
		reads++
		if reads == 1 {
			return nil, errors.New("未读取到当前禅道域名 Cookie")
		}
		return nil, errChromeDebugUnavailable
	}
	verifyCapturedZentaoSession = func(baseURL string, sessionHeader string) error {
		t.Fatalf("verify should not be called after browser closes")
		return nil
	}
	u, err := url.Parse("http://zentao.example.com/")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = waitForVerifiedZentaoSession(ctx, 12345, u)
	if err == nil {
		t.Fatal("waitForVerifiedZentaoSession succeeded after browser closed")
	}
	if !strings.Contains(err.Error(), "登录窗口已关闭") {
		t.Fatalf("err = %v", err)
	}
	if reads != 2 {
		t.Fatalf("reads = %d, want 2", reads)
	}
}

func TestWaitForVerifiedZentaoSessionStopsWhenChromeHasNoPageTargets(t *testing.T) {
	oldRead := readZentaoCookies
	oldVerify := verifyCapturedZentaoSession
	oldPollInterval := zentaoLoginPollInterval
	t.Cleanup(func() {
		readZentaoCookies = oldRead
		verifyCapturedZentaoSession = oldVerify
		zentaoLoginPollInterval = oldPollInterval
	})
	zentaoLoginPollInterval = time.Millisecond
	reads := 0
	readZentaoCookies = func(port int, host string) ([]chromeCookie, error) {
		reads++
		if reads == 1 {
			return nil, errors.New("未读取到当前禅道域名 Cookie")
		}
		return nil, errChromePageUnavailable
	}
	verifyCapturedZentaoSession = func(baseURL string, sessionHeader string) error {
		t.Fatalf("verify should not be called after page closes")
		return nil
	}
	u, err := url.Parse("http://zentao.example.com/")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = waitForVerifiedZentaoSession(ctx, 12345, u)
	if err == nil {
		t.Fatal("waitForVerifiedZentaoSession succeeded after page closed")
	}
	if !strings.Contains(err.Error(), "登录窗口已关闭") {
		t.Fatalf("err = %v", err)
	}
	if reads != 2 {
		t.Fatalf("reads = %d, want 2", reads)
	}
}

func TestWaitForVerifiedZentaoSessionStopsWhenBrowserNeverStartsAfterGrace(t *testing.T) {
	oldRead := readZentaoCookies
	oldPollInterval := zentaoLoginPollInterval
	oldStartupGrace := chromeLoginStartupGrace
	t.Cleanup(func() {
		readZentaoCookies = oldRead
		zentaoLoginPollInterval = oldPollInterval
		chromeLoginStartupGrace = oldStartupGrace
	})
	zentaoLoginPollInterval = time.Millisecond
	chromeLoginStartupGrace = time.Millisecond
	readZentaoCookies = func(port int, host string) ([]chromeCookie, error) {
		return nil, errChromeDebugUnavailable
	}
	u, err := url.Parse("http://zentao.example.com/")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = waitForVerifiedZentaoSession(ctx, 12345, u)
	if err == nil {
		t.Fatal("waitForVerifiedZentaoSession succeeded when browser never started")
	}
	if !strings.Contains(err.Error(), "登录窗口已关闭") {
		t.Fatalf("err = %v", err)
	}
}
