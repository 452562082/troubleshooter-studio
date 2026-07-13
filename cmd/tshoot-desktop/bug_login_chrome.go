package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

var captureZentaoLoginSession = captureZentaoLoginSessionChrome
var recaptureZentaoLoginSession = recaptureZentaoLoginSessionChrome
var readZentaoCookies = readChromeCookies
var verifyCapturedZentaoSession = verifyZentaoSession
var errChromeDebugUnavailable = errors.New("chrome debug endpoint unavailable")
var errChromePageUnavailable = errors.New("chrome page unavailable")
var zentaoLoginPollInterval = 2 * time.Second
var chromeLoginStartupGrace = 15 * time.Second
var zentaoSilentRecaptureTimeout = 25 * time.Second

type chromeDebugTarget struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	URL                  string `json:"url"`
	Type                 string `json:"type"`
}

type chromeDebugVersion struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type chromeCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

type cdpClient struct {
	conn *websocket.Conn
	next atomic.Int64
}

func captureZentaoLoginSessionChrome(baseURL string) (string, int, error) {
	return captureZentaoLoginSessionChromeWithTimeout(baseURL, 5*time.Minute)
}

func recaptureZentaoLoginSessionChrome(baseURL string) (string, int, error) {
	return captureZentaoLoginSessionChromeWithTimeout(baseURL, zentaoSilentRecaptureTimeout)
}

func captureZentaoLoginSessionChromeWithTimeout(baseURL string, timeout time.Duration) (string, int, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", 0, fmt.Errorf("invalid zentao base url %q", baseURL)
	}
	port, err := freeLocalPort()
	if err != nil {
		return "", 0, err
	}
	profileDir, err := zentaoBrowserProfileDir(baseURL)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return "", 0, err
	}
	browserCmd, err := openControlledBrowser(u.String(), port, profileDir)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		_ = closeChromeDebugBrowser(port)
		if browserCmd != nil && browserCmd.Process != nil {
			_ = browserCmd.Process.Kill()
			_, _ = browserCmd.Process.Wait()
		}
	}()
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	header, cookieCount, err := waitForVerifiedZentaoSession(ctx, port, u)
	if err != nil {
		return "", 0, err
	}
	if header == "" {
		return "", 0, errors.New("未读取到禅道登录态 Cookie")
	}
	return header, cookieCount, nil
}

func verifyZentaoSession(baseURL string, sessionHeader string) error {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("invalid zentao base url %q", baseURL)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api.php/v1/bugs"
	q := base.Query()
	q.Set("limit", "1")
	base.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, base.String(), nil)
	if err != nil {
		return err
	}
	if err := applySessionHeader(req.Header, sessionHeader); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err := verifyZentaoWebSession(baseURL, sessionHeader); err == nil {
			return nil
		}
		return fmt.Errorf("禅道登录态校验失败: %s", resp.Status)
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		if err := verifyZentaoWebSession(baseURL, sessionHeader); err == nil {
			return nil
		}
		return fmt.Errorf("禅道登录态校验返回非 JSON: %w", err)
	}
	if _, ok := payload["bugs"]; ok {
		return nil
	}
	if _, ok := payload["data"]; ok {
		return nil
	}
	return errors.New("禅道登录态校验未返回 Bug 数据,可能尚未完成登录")
}

func verifyZentaoWebSession(baseURL string, sessionHeader string) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSpace(baseURL), nil)
	if err != nil {
		return err
	}
	if err := applySessionHeader(req.Header, sessionHeader); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("禅道 Web 登录态校验失败: %s", resp.Status)
	}
	finalURL := strings.ToLower(resp.Request.URL.String())
	if strings.Contains(finalURL, "login") || strings.Contains(finalURL, "user-login") {
		return errors.New("禅道 Web 登录态仍在登录页")
	}
	return nil
}

func applySessionHeader(header http.Header, raw string) error {
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("invalid header line %q", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return fmt.Errorf("invalid header line %q", line)
		}
		header.Set(key, value)
	}
	return nil
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func openControlledBrowser(rawURL string, port int, profileDir string) (*exec.Cmd, error) {
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--new-window",
		rawURL,
	}
	switch runtime.GOOS {
	case "darwin":
		for _, exe := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		} {
			cmd := exec.Command(exe, args...)
			if err := cmd.Start(); err == nil {
				return cmd, nil
			}
		}
	case "windows":
		for _, exe := range []string{"chrome.exe", "msedge.exe"} {
			cmd := exec.Command(exe, args...)
			if err := cmd.Start(); err == nil {
				return cmd, nil
			}
		}
	default:
		for _, exe := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"} {
			cmd := exec.Command(exe, args...)
			if err := cmd.Start(); err == nil {
				return cmd, nil
			}
		}
	}
	return nil, fmt.Errorf("无法启动受控浏览器,请安装 Google Chrome 或 Microsoft Edge")
}

func waitForVerifiedZentaoSession(ctx context.Context, port int, baseURL *url.URL) (string, int, error) {
	ticker := time.NewTicker(zentaoLoginPollInterval)
	defer ticker.Stop()
	var lastErr error
	seenBrowser := false
	startedAt := time.Now()
	for {
		header, count, err := readAndVerifyZentaoSession(port, baseURL)
		if err == nil {
			return header, count, nil
		}
		if isChromeWindowClosedErr(err) && (seenBrowser || time.Since(startedAt) >= chromeLoginStartupGrace) {
			return "", 0, fmt.Errorf("登录窗口已关闭,请重新点击登录: %w", err)
		}
		if !errors.Is(err, errChromeDebugUnavailable) {
			seenBrowser = true
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", 0, fmt.Errorf("等待禅道登录超时: %w", lastErr)
			}
			return "", 0, errors.New("等待禅道登录超时")
		case <-ticker.C:
		}
	}
}

func isChromeWindowClosedErr(err error) bool {
	return errors.Is(err, errChromeDebugUnavailable) || errors.Is(err, errChromePageUnavailable)
}

func readAndVerifyZentaoSession(port int, baseURL *url.URL) (string, int, error) {
	cookies, err := readZentaoCookies(port, baseURL.Hostname())
	if err != nil {
		return "", 0, err
	}
	header := cookieHeaderForHost(cookies, baseURL.Hostname())
	if header == "" {
		return "", 0, errors.New("未读取到当前禅道域名 Cookie")
	}
	header = "Cookie: " + header
	if err := verifyCapturedZentaoSession(baseURL.String(), header); err != nil {
		return "", 0, err
	}
	return header, len(cookies), nil
}

func readChromeCookies(port int, host string) ([]chromeCookie, error) {
	targets, err := chromeTargets(port)
	if err != nil {
		return nil, err
	}
	pageTargets := 0
	for _, target := range targets {
		if target.Type != "page" || target.WebSocketDebuggerURL == "" {
			continue
		}
		pageTargets++
		client, err := newCDPClient(target.WebSocketDebuggerURL)
		if err != nil {
			continue
		}
		cookies, err := client.getAllCookies()
		_ = client.conn.Close()
		if err != nil {
			continue
		}
		matched := matchingCookies(cookies, host)
		if len(matched) > 0 {
			return matched, nil
		}
	}
	if pageTargets == 0 {
		return nil, errChromePageUnavailable
	}
	return nil, errors.New("未读取到当前禅道域名 Cookie")
}

func closeChromeDebugBrowser(port int) error {
	if wsURL, err := chromeBrowserWebSocketURL(port); err == nil && wsURL != "" {
		client, err := newCDPClient(wsURL)
		if err == nil {
			err = client.call("Browser.close", nil, nil)
			_ = client.conn.Close()
			if err == nil {
				return nil
			}
		}
	}
	targets, err := chromeTargets(port)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.WebSocketDebuggerURL == "" {
			continue
		}
		client, err := newCDPClient(target.WebSocketDebuggerURL)
		if err != nil {
			continue
		}
		err = client.call("Browser.close", nil, nil)
		_ = client.conn.Close()
		if err == nil {
			return nil
		}
	}
	return errChromePageUnavailable
}

func chromeBrowserWebSocketURL(port int) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errChromeDebugUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: %s", errChromeDebugUnavailable, resp.Status)
	}
	var version chromeDebugVersion
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", err
	}
	return version.WebSocketDebuggerURL, nil
}

func chromeTargets(port int) ([]chromeDebugTarget, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json", port))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errChromeDebugUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %s", errChromeDebugUnavailable, resp.Status)
	}
	var targets []chromeDebugTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func newCDPClient(wsURL string) (*cdpClient, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	return &cdpClient{conn: conn}, nil
}

func (c *cdpClient) call(method string, params map[string]any, out any) error {
	id := c.next.Add(1)
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.conn.WriteJSON(msg); err != nil {
		return err
	}
	for {
		var resp struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := c.conn.ReadJSON(&resp); err != nil {
			return err
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return errors.New(resp.Error.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
}

func (c *cdpClient) getAllCookies() ([]chromeCookie, error) {
	var result struct {
		Cookies []chromeCookie `json:"cookies"`
	}
	if err := c.call("Network.getAllCookies", nil, &result); err != nil {
		return nil, err
	}
	return result.Cookies, nil
}

func matchingCookies(cookies []chromeCookie, host string) []chromeCookie {
	out := make([]chromeCookie, 0, len(cookies))
	for _, c := range cookies {
		if strings.TrimSpace(c.Name) == "" || !cookieDomainMatchesHost(c.Domain, host) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func cookieHeaderForHost(cookies []chromeCookie, host string) string {
	parts := make([]string, 0, len(cookies))
	for _, c := range matchingCookies(cookies, host) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func cookieDomainMatchesHost(domain string, host string) bool {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	host = strings.ToLower(strings.TrimSpace(host))
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

func hasLikelyLoginCookie(cookies []chromeCookie) bool {
	for _, c := range cookies {
		name := strings.ToLower(c.Name)
		if strings.Contains(name, "sid") || strings.Contains(name, "session") ||
			strings.Contains(name, "token") || strings.Contains(name, "auth") ||
			strings.Contains(name, "zentao") {
			return true
		}
	}
	return false
}

func controlledBrowserProfileRoot() string {
	return filepath.Join(bughub.DefaultRoot(), "browser-profiles")
}

func zentaoBrowserProfileDir(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid zentao base url %q", baseURL)
	}
	return filepath.Join(controlledBrowserProfileRoot(), safeProfileName(u.Host)), nil
}

func removeZentaoBrowserProfile(baseURL string) error {
	dir, err := zentaoBrowserProfileDir(baseURL)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func safeProfileName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		return "default"
	}
	return name
}
