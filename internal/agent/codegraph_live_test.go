package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const codeGraphLiveTimeout = 5 * time.Minute

func TestCodeGraphLive(t *testing.T) {
	if os.Getenv("TSHOOT_CODEGRAPH_LIVE") != "1" {
		t.Skip("set TSHOOT_CODEGRAPH_LIVE=1 to download/run pinned CodeGraph")
	}

	started := time.Now()
	var installLogs []string
	binary, err := EnsureCodeGraphInstalled(func(line string) {
		installLogs = append(installLogs, line)
		t.Log(line)
	})
	if err != nil {
		t.Fatalf("install pinned CodeGraph %s: %v", codeGraphVersion, err)
	}
	t.Logf("pinned CodeGraph %s ready in %s at %s", codeGraphVersion, time.Since(started).Round(time.Millisecond), binary)
	if len(installLogs) == 0 {
		t.Fatal("EnsureCodeGraphInstalled emitted no cache/download evidence")
	}

	root := t.TempDir()
	goRepo := filepath.Join(root, "go-fixture")
	javaRepo := filepath.Join(root, "java-fixture")
	initCodeGraphLiveRepo(t, goRepo, map[string]string{
		"go.mod": "module example.invalid/codegraphlive\n\ngo 1.22\n",
		"fixture.go": `package codegraphlive

func GoGraphEntryUnique() string {
	return GoGraphLeafUnique()
}

func GoGraphLeafUnique() string {
	return "go-live-source-line-unique"
}
`,
	})
	initCodeGraphLiveRepo(t, javaRepo, map[string]string{
		"src/main/java/livefixture/JavaGraphEntryUnique.java": `package livefixture;

public final class JavaGraphEntryUnique {
    public String run() {
        return JavaGraphLeafUnique.value();
    }
}
`,
		"src/main/java/livefixture/JavaGraphLeafUnique.java": `package livefixture;

public final class JavaGraphLeafUnique {
    private JavaGraphLeafUnique() {}

    public static String value() {
        return "java-live-source-line-unique";
    }
}
`,
	})

	mcp := startCodeGraphLiveMCP(t, binary)
	defer mcp.close()

	tools := mcp.initializeAndListTools(t)
	if !containsMCPTool(tools, "codegraph_explore") {
		t.Fatalf("runtime tools/list = %v, want codegraph_explore", tools)
	}
	t.Logf("runtime tools/list: %v", tools)

	fallback := mcp.callTool(t, "codegraph_explore", map[string]any{
		"query":       "JavaGraphEntryUnique JavaGraphLeafUnique",
		"projectPath": javaRepo,
		"maxFiles":    4,
	})
	assertCodeGraphLiveContains(t, fallback,
		"isn't indexed with codegraph",
		"Use your built-in tools",
		"don't call codegraph for it again this session",
	)

	systemID := fmt.Sprintf("codegraph-live-%d", time.Now().UnixNano())
	InvalidateCodeGraphIndexCache(systemID)
	report := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath:     binary,
		SystemID:       systemID,
		Repos:          []CodeGraphRepoTarget{{Name: "go-live", Path: goRepo}, {Name: "java-live", Path: javaRepo}},
		InitTimeout:    codeGraphLiveTimeout,
		SyncTimeout:    time.Minute,
		MaxConcurrency: 1,
		OnProgress:     func(line string) { t.Log(line) },
	})
	if report.Ready != 2 || report.Total != 2 || len(report.Repos) != 2 {
		t.Fatalf("live index report = %#v, want two ready repositories", report)
	}
	for _, repo := range report.Repos {
		if repo.Action != "initialized" || repo.Status != "ready" || repo.IndexState != "complete" || repo.FileCount <= 0 || repo.NodeCount <= 0 {
			t.Errorf("live index result = %#v, want initialized complete index with non-zero files/nodes", repo)
		}
	}

	for _, repoPath := range []string{goRepo, javaRepo} {
		status, statusErr := queryCodeGraphStatus(context.Background(), binary, repoPath, time.Minute)
		if statusErr != nil {
			t.Fatalf("status %s: %v", repoPath, statusErr)
		}
		if !codeGraphStatusReady(status) {
			t.Fatalf("status %s = %#v, want complete non-zero index", repoPath, status)
		}
		t.Logf("status %s: state=%s files=%d nodes=%d edges=%d", filepath.Base(repoPath), status.Index.State, status.FileCount, status.NodeCount, status.EdgeCount)
	}

	goExplore := mcp.callTool(t, "codegraph_explore", map[string]any{
		"query":       "GoGraphEntryUnique GoGraphLeafUnique",
		"projectPath": goRepo,
		"maxFiles":    4,
	})
	assertCodeGraphLiveContains(t, goExplore, "GoGraphEntryUnique", "return GoGraphLeafUnique()", "go-live-source-line-unique")
	assertCodeGraphLiveSourceLine(t, goExplore, "return GoGraphLeafUnique()")

	javaExplore := mcp.callTool(t, "codegraph_explore", map[string]any{
		"query":       "JavaGraphEntryUnique JavaGraphLeafUnique",
		"projectPath": javaRepo,
		"maxFiles":    4,
	})
	assertCodeGraphLiveContains(t, javaExplore, "JavaGraphEntryUnique", "return JavaGraphLeafUnique.value();", "java-live-source-line-unique")
	assertCodeGraphLiveSourceLine(t, javaExplore, "return JavaGraphLeafUnique.value();")

	goSource := filepath.Join(goRepo, "fixture.go")
	contents, err := os.ReadFile(goSource)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, []byte(`
func GoGraphFreshAfterSyncUnique() string {
	return GoGraphEntryUnique()
}
`)...)
	if err := os.WriteFile(goSource, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "sync", goRepo).CombinedOutput(); err != nil {
		t.Fatalf("codegraph sync %s: %v\n%s", goRepo, err, output)
	}
	freshExplore := mcp.callTool(t, "codegraph_explore", map[string]any{
		"query":       "GoGraphFreshAfterSyncUnique GoGraphEntryUnique",
		"projectPath": goRepo,
		"maxFiles":    4,
	})
	assertCodeGraphLiveContains(t, freshExplore, "GoGraphFreshAfterSyncUnique", "return GoGraphEntryUnique()")
	assertCodeGraphLiveSourceLine(t, freshExplore, "return GoGraphEntryUnique()")

	beforeBranch := codeGraphLiveGit(t, goRepo, "branch", "--show-current")
	beforeHead := codeGraphLiveGit(t, goRepo, "rev-parse", "HEAD")
	comparison, err := compareCodeGraphLiveBranch(goRepo, "release/prod")
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Mismatch || comparison.Current != "main" || comparison.Target != "release/prod" || comparison.Freshness != "branch-mismatch" {
		t.Fatalf("branch comparison = %#v, want main vs release/prod mismatch", comparison)
	}
	if after := codeGraphLiveGit(t, goRepo, "branch", "--show-current"); after != beforeBranch {
		t.Fatalf("read-only branch comparison changed branch from %q to %q", beforeBranch, after)
	}
	if after := codeGraphLiveGit(t, goRepo, "rev-parse", "HEAD"); after != beforeHead {
		t.Fatalf("read-only branch comparison changed HEAD from %q to %q", beforeHead, after)
	}
}

type codeGraphLiveBranchComparison struct {
	Current   string
	Target    string
	Freshness string
	Mismatch  bool
}

func compareCodeGraphLiveBranch(repoPath, target string) (codeGraphLiveBranchComparison, error) {
	output, err := exec.Command("git", "-C", repoPath, "branch", "--show-current").CombinedOutput()
	if err != nil {
		return codeGraphLiveBranchComparison{}, fmt.Errorf("read current branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	current := strings.TrimSpace(string(output))
	mismatch := current != target
	freshness := "fresh"
	if mismatch {
		freshness = "branch-mismatch"
	}
	return codeGraphLiveBranchComparison{Current: current, Target: target, Freshness: freshness, Mismatch: mismatch}, nil
}

func initCodeGraphLiveRepo(t *testing.T, repoPath string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	codeGraphLiveGit(t, repoPath, "init", "-q", "-b", "main")
	for name, contents := range files {
		path := filepath.Join(repoPath, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	codeGraphLiveGit(t, repoPath, "add", ".")
	codeGraphLiveGit(t, repoPath, "-c", "user.name=CodeGraph Live", "-c", "user.email=codegraph-live@example.invalid", "commit", "-qm", "initial fixture")
	codeGraphLiveGit(t, repoPath, "branch", "release/prod")
}

func codeGraphLiveGit(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repoPath}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(commandArgs, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

type codeGraphLiveMCP struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	stderr codeGraphLiveBuffer
	nextID int
	once   sync.Once
}

func startCodeGraphLiveMCP(t *testing.T, binary string) *codeGraphLiveMCP {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), codeGraphLiveTimeout)
	client := &codeGraphLiveMCP{ctx: ctx, cancel: cancel, nextID: 1}
	client.cmd = exec.CommandContext(ctx, binary, "serve", "--mcp")
	client.cmd.Env = append(os.Environ(), "CODEGRAPH_TELEMETRY=0", "DO_NOT_TRACK=1", "CODEGRAPH_NO_WATCH=1")
	setProcessGroup(client.cmd)
	stdin, err := client.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := client.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client.stdin = stdin
	client.reader = bufio.NewReader(stdout)
	client.cmd.Stderr = &client.stderr
	if err := client.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start CodeGraph MCP: %v", err)
	}
	return client
}

func (c *codeGraphLiveMCP) initializeAndListTools(t *testing.T) []string {
	t.Helper()
	initResponse := c.request(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tshoot-codegraph-live", "version": "0"},
	})
	if _, ok := initResponse["result"].(map[string]any); !ok {
		t.Fatalf("initialize response missing result: %#v", initResponse)
	}
	if err := writeJSONLine(c.stdin, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatalf("send notifications/initialized: %v", err)
	}
	response := c.request(t, "tools/list", map[string]any{})
	result, _ := response["result"].(map[string]any)
	items, _ := result["tools"].([]any)
	tools := make([]string, 0, len(items))
	for _, item := range items {
		tool, _ := item.(map[string]any)
		if name, ok := tool["name"].(string); ok {
			tools = append(tools, name)
		}
	}
	return tools
}

func (c *codeGraphLiveMCP) callTool(t *testing.T, name string, arguments map[string]any) string {
	t.Helper()
	response := c.request(t, "tools/call", map[string]any{"name": name, "arguments": arguments})
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s response missing result: %#v", name, response)
	}
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tools/call %s returned isError: %#v", name, result)
	}
	content, _ := result["content"].([]any)
	var text strings.Builder
	for _, item := range content {
		part, _ := item.(map[string]any)
		if value, ok := part["text"].(string); ok {
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(value)
		}
	}
	if text.Len() == 0 {
		t.Fatalf("tools/call %s returned no text content: %#v", name, result)
	}
	return text.String()
}

func (c *codeGraphLiveMCP) request(t *testing.T, method string, params map[string]any) map[string]any {
	t.Helper()
	id := c.nextID
	c.nextID++
	if err := writeJSONLine(c.stdin, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		t.Fatalf("send MCP %s: %v (stderr: %s)", method, err, c.stderr.tail())
	}
	for {
		response, err := readJSONLine(c.ctx, c.reader)
		if err != nil {
			t.Fatalf("read MCP %s response: %v (stderr: %s)", method, err, c.stderr.tail())
		}
		responseID, hasID := response["id"].(float64)
		if !hasID || int(responseID) != id {
			continue
		}
		if rpcError, ok := response["error"].(map[string]any); ok {
			t.Fatalf("MCP %s error: %#v (stderr: %s)", method, rpcError, c.stderr.tail())
		}
		return response
	}
}

func (c *codeGraphLiveMCP) close() {
	c.once.Do(func() {
		c.cancel()
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			killProcessGroup(c.cmd.Process.Pid)
			_ = c.cmd.Process.Kill()
			_ = c.cmd.Wait()
		}
	})
}

type codeGraphLiveBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *codeGraphLiveBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *codeGraphLiveBuffer) tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := b.buf.String()
	if len(value) > 1000 {
		value = value[len(value)-1000:]
	}
	return value
}

func assertCodeGraphLiveContains(t *testing.T, text string, snippets ...string) {
	t.Helper()
	for _, snippet := range snippets {
		if !strings.Contains(text, snippet) {
			t.Fatalf("CodeGraph response missing %q:\n%s", snippet, text)
		}
	}
}

func assertCodeGraphLiveSourceLine(t *testing.T, text, source string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) != source {
			continue
		}
		var lineNumber int
		if _, err := fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &lineNumber); err == nil && lineNumber > 0 {
			return
		}
	}
	t.Fatalf("CodeGraph response did not expose a numbered source line %q:\n%s", source, text)
}
