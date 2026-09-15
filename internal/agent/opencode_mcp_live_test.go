package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/aitools"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func TestOpenCodeMCPProtocolLive(t *testing.T) {
	if os.Getenv("TSHOOT_LIVE_OPENCODE_PROTOCOL") != "1" {
		t.Skip("explicit real CLI opt-in")
	}
	binary, err := aitools.FindOpenCodeCLI()
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_DISABLE_DEFAULT_PLUGINS", "true")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "true")
	script := filepath.Join(root, "mcp.py")
	ledger := filepath.Join(root, "mcp.log")
	code := `import sys, json, os
for line in sys.stdin:
 msg=json.loads(line)
 method=msg.get('method')
 with open(os.environ['LEDGER'],'a') as f: f.write(method+'\n')
 if 'id' not in msg: continue
 if method=='initialize': result={'protocolVersion':'2024-11-05','capabilities':{'tools':{}},'serverInfo':{'name':'fixture','version':'1'}}
 elif method=='tools/list': result={'tools':[{'name':'read_status','description':'Local fixture','inputSchema':{'type':'object','properties':{}}}]}
 else: result={}
 print(json.dumps({'jsonrpc':'2.0','id':msg['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(script, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	servers, err := openCodeMCPServers(map[string]any{"studio-fixture": map[string]any{"command": python, "args": []string{script}, "env": map[string]any{"LEDGER": ledger}}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := TargetOpenCode.MCPConfigPath(root)
	if _, err := updateOpenCodeMCP(configPath, "studio-", servers, false); err != nil {
		t.Fatal(err)
	}
	if err := probeOpenCodeMCP(configPath, "studio-", func(s string) { t.Log(s) }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp", "list")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenCode MCP list failed: %v: %s", err, output)
	}
	data, err := os.ReadFile(ledger)
	if err != nil || !strings.Contains(string(data), "initialize") || !strings.Contains(string(data), "tools/list") {
		t.Fatalf("actual CLI did not initialize and list tools: %s err=%v output=%s", data, err, output)
	}
	t.Log("real OpenCode loaded converted MCP configuration; initialize + tools/list PASS")

	cfg, err := config.Load("../../examples/shop-troubleshooter.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "generated")
	g := generator.New(cfg, "../../templates", out)
	if err := g.GenerateOpenCode(); err != nil {
		t.Fatal(err)
	}
	if err := InstallNative(out+"-opencode", "opencode"); err != nil {
		t.Fatal(err)
	}
	list := exec.CommandContext(ctx, binary, "agent", "list")
	list.Dir = root
	agents, err := list.CombinedOutput()
	if err != nil || !strings.Contains(string(agents), "shop-bot") || !strings.Contains(string(agents), "shop-fixer") || strings.Contains(string(agents), "shop-validator") {
		t.Fatalf("generated agents not recognized: %v: %s", err, agents)
	}
	t.Log("real OpenCode recognizes both generated and installed Agent definitions PASS")
}
