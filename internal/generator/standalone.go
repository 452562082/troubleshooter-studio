package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateStandalone 输出独立 Web 聊天格式：
//   - system-prompt.md（所有排障知识合并为一个 system prompt）
//   - skills/（映射表 + 脚本）
//   - scripts/（辅助脚本）
//   - server.py（最小化 Python 聊天服务，调 Claude/OpenAI API）
//   - index.html（简单聊天前端）
//   - requirements.txt（Python 依赖）
//   - Dockerfile（容器镜像定义）
//   - docker-compose.yaml（一键启动）
//   - install.sh（本机一键安装：venv + pip install + 启动提示）
//   - README.md（使用说明）
func (g *Generator) GenerateStandalone() error {
	outDir := g.OutputDir + "-standalone"
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	// 先渲染到临时目录
	tmpDir, err := os.MkdirTemp("", "factory-standalone-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	origOut := g.OutputDir
	g.OutputDir = tmpDir
	if err := g.Generate(); err != nil {
		g.OutputDir = origOut
		return fmt.Errorf("render templates: %w", err)
	}
	g.OutputDir = origOut

	wsRoot := filepath.Join(tmpDir, "templates", "workspace-template")

	// 1) system-prompt.md
	prompt, err := buildSystemPrompt(wsRoot, g.Ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "system-prompt.md"), []byte(prompt), 0o644); err != nil {
		return err
	}

	// 2) 拷贝 skills + scripts
	skillsDir := filepath.Join(wsRoot, "skills")
	if err := copyDirRecursive(skillsDir, filepath.Join(outDir, "skills")); err != nil {
		return err
	}
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, filepath.Join(outDir, "scripts")); err != nil {
			return err
		}
	}

	// 3) server.py
	if err := os.WriteFile(filepath.Join(outDir, "server.py"), []byte(serverPy(g.Ctx)), 0o644); err != nil {
		return err
	}

	// 4) index.html
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(indexHTML(g.Ctx)), 0o644); err != nil {
		return err
	}

	// 5) requirements.txt
	if err := os.WriteFile(filepath.Join(outDir, "requirements.txt"), []byte(requirementsTxt()), 0o644); err != nil {
		return err
	}

	// 6) Dockerfile
	if err := os.WriteFile(filepath.Join(outDir, "Dockerfile"), []byte(dockerfile()), 0o644); err != nil {
		return err
	}

	// 7) docker-compose.yaml
	if err := os.WriteFile(filepath.Join(outDir, "docker-compose.yaml"), []byte(dockerCompose(g.Ctx)), 0o644); err != nil {
		return err
	}

	// 8) install.sh（本机一键安装）
	if err := os.WriteFile(filepath.Join(outDir, "install.sh"), []byte(standaloneInstallSh(g.Ctx)), 0o755); err != nil {
		return err
	}

	// 9) README.md
	if err := os.WriteFile(filepath.Join(outDir, "README.md"), []byte(standaloneReadme(g.Ctx)), 0o644); err != nil {
		return err
	}

	return nil
}

func buildSystemPrompt(wsRoot string, ctx *Context) (string, error) {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s 排障机器人 System Prompt\n\n", ctx.System.Name))

	for _, name := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md"} {
		if data, err := os.ReadFile(filepath.Join(wsRoot, name)); err == nil {
			sb.Write(data)
			sb.WriteString("\n\n---\n\n")
		}
	}

	// 嵌入所有 skill 的 SKILL.md
	sb.WriteString("# Skills 详细说明\n\n")
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if data, err := os.ReadFile(skillMD); err == nil {
			sb.WriteString(fmt.Sprintf("## skill: %s\n\n", e.Name()))
			sb.Write(data)
			sb.WriteString("\n\n---\n\n")
		}
	}

	return sb.String(), nil
}

func serverPy(ctx *Context) string {
	return fmt.Sprintf(`#!/usr/bin/env python3
"""
%s 排障机器人 — 独立 Web 聊天服务
由 troubleshooter-factory 生成

用法：
  export LLM_API_KEY=your-api-key
  export LLM_BASE_URL=https://api.anthropic.com  # 或 OpenAI 兼容端点
  python3 server.py

依赖：pip install anthropic flask
"""
import os
import json
from pathlib import Path

try:
    from flask import Flask, request, jsonify, send_file
except ImportError:
    print("请安装依赖：pip install flask anthropic")
    exit(1)

try:
    import anthropic
except ImportError:
    anthropic = None

app = Flask(__name__)

# 加载 system prompt
PROMPT_PATH = Path(__file__).parent / "system-prompt.md"
SYSTEM_PROMPT = PROMPT_PATH.read_text(encoding="utf-8") if PROMPT_PATH.exists() else "你是一个排障机器人。"

@app.route("/")
def index():
    return send_file("index.html")

@app.route("/api/chat", methods=["POST"])
def chat():
    data = request.json
    messages = data.get("messages", [])

    api_key = os.environ.get("LLM_API_KEY", "")
    if not api_key:
        return jsonify({"error": "请设置 LLM_API_KEY 环境变量"}), 400

    if anthropic:
        client = anthropic.Anthropic(api_key=api_key)
        resp = client.messages.create(
            model="claude-sonnet-4-20250514",
            max_tokens=4096,
            system=SYSTEM_PROMPT,
            messages=messages,
        )
        return jsonify({"content": resp.content[0].text})
    else:
        return jsonify({"error": "请安装 anthropic SDK：pip install anthropic"}), 500

if __name__ == "__main__":
    port = int(os.environ.get("PORT", 3000))
    print(f"%s 排障机器人启动：http://localhost:{port}")
    app.run(host="0.0.0.0", port=port, debug=False)
`, ctx.System.Name, ctx.System.Name)
}

func indexHTML(ctx *Context) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>%s 排障机器人</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif; background: #f8fafc; height: 100vh; display: flex; flex-direction: column; }
  .header { background: #1e293b; color: white; padding: 16px 24px; font-size: 16px; font-weight: 600; }
  .header span { font-size: 12px; color: #94a3b8; margin-left: 8px; }
  .messages { flex: 1; overflow-y: auto; padding: 20px 24px; }
  .msg { max-width: 80%%; margin-bottom: 16px; padding: 12px 16px; border-radius: 12px; line-height: 1.6; font-size: 14px; white-space: pre-wrap; }
  .msg.user { background: #3b82f6; color: white; margin-left: auto; border-bottom-right-radius: 4px; }
  .msg.bot { background: white; color: #1e293b; border: 1px solid #e2e8f0; border-bottom-left-radius: 4px; }
  .input-bar { display: flex; gap: 8px; padding: 16px 24px; background: white; border-top: 1px solid #e2e8f0; }
  .input-bar textarea { flex: 1; padding: 10px 14px; border: 1px solid #d1d5db; border-radius: 8px; font-size: 14px; resize: none; height: 44px; font-family: inherit; }
  .input-bar textarea:focus { outline: none; border-color: #3b82f6; }
  .input-bar button { padding: 10px 20px; background: #3b82f6; color: white; border: none; border-radius: 8px; font-size: 14px; cursor: pointer; font-weight: 600; }
  .input-bar button:hover { background: #2563eb; }
  .input-bar button:disabled { background: #94a3b8; }
  .typing { color: #94a3b8; font-style: italic; padding: 8px 16px; }
</style>
</head>
<body>
<div class="header">%s 排障机器人 <span>由 troubleshooter-factory 生成</span></div>
<div class="messages" id="messages">
  <div class="msg bot">你好，我是 %s 排障机器人。请描述你遇到的问题，包括：环境（dev/prod）、服务名、错误现象。</div>
</div>
<div class="input-bar">
  <textarea id="input" placeholder="描述问题..." onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send()}"></textarea>
  <button onclick="send()" id="btn">发送</button>
</div>
<script>
const messages = [];
async function send() {
  const input = document.getElementById('input');
  const text = input.value.trim();
  if (!text) return;
  input.value = '';
  messages.push({role: 'user', content: text});
  addMsg('user', text);
  document.getElementById('btn').disabled = true;
  try {
    const resp = await fetch('/api/chat', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({messages})
    });
    const data = await resp.json();
    if (data.error) { addMsg('bot', '❌ ' + data.error); }
    else { messages.push({role: 'assistant', content: data.content}); addMsg('bot', data.content); }
  } catch(e) { addMsg('bot', '❌ 请求失败：' + e.message); }
  document.getElementById('btn').disabled = false;
}
function addMsg(role, text) {
  const div = document.createElement('div');
  div.className = 'msg ' + role;
  div.textContent = text;
  document.getElementById('messages').appendChild(div);
  div.scrollIntoView({behavior: 'smooth'});
}
</script>
</body>
</html>`, ctx.System.Name, ctx.System.Name, ctx.System.Name)
}

func dockerCompose(ctx *Context) string {
	return fmt.Sprintf(`# %s 排障机器人 — 独立部署
# 用法：LLM_API_KEY=your-key docker compose up --build
version: "3.8"
services:
  troubleshooter:
    build: .
    ports:
      - "3000:3000"
    environment:
      - LLM_API_KEY=${LLM_API_KEY}
      - PORT=3000
    volumes:
      - ./skills:/app/skills:ro
      - ./scripts:/app/scripts:ro
    restart: unless-stopped
`, ctx.System.Name)
}

func dockerfile() string {
	return `# 由 troubleshooter-factory 生成
FROM python:3.11-slim

WORKDIR /app

# 先装依赖，利用层缓存
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 拷贝代码、prompt、资产
COPY server.py index.html system-prompt.md ./
COPY skills ./skills
COPY scripts ./scripts

EXPOSE 3000

CMD ["python3", "server.py"]
`
}

func requirementsTxt() string {
	return `# troubleshooter-factory standalone runtime
flask>=3.0
anthropic>=0.34
`
}

func standaloneInstallSh(ctx *Context) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
# %s 排障机器人 — 本机一键安装（venv + pip install）
# 由 troubleshooter-factory 生成
set -euo pipefail

cd "$(dirname "$0")"

if ! command -v python3 >/dev/null 2>&1; then
  echo "错误：未找到 python3，请先安装 Python 3.9+" >&2
  exit 1
fi

VENV=".venv"
if [ ! -d "$VENV" ]; then
  echo "→ 创建虚拟环境 $VENV"
  python3 -m venv "$VENV"
fi

# shellcheck disable=SC1091
source "$VENV/bin/activate"

echo "→ 升级 pip"
pip install --quiet --upgrade pip

echo "→ 安装依赖 (flask + anthropic)"
pip install --quiet -r requirements.txt

echo ""
echo "✓ 安装完成。下一步："
echo ""
echo "  1. 设置 LLM API key："
echo "       export LLM_API_KEY=<your-anthropic-api-key>"
echo ""
echo "  2. 激活 venv 并启动："
echo "       source $VENV/bin/activate"
echo "       python3 server.py"
echo ""
echo "  → 访问 http://localhost:3000"
echo ""
echo "  或使用 Docker："
echo "       LLM_API_KEY=<your-key> docker compose up --build"
`, ctx.System.Name)
}

func standaloneReadme(ctx *Context) string {
	return fmt.Sprintf(`# %s 排障机器人（独立部署版）

由 troubleshooter-factory 生成。不依赖 OpenClaw / Claude Code / Cursor，只需一个 LLM API key。

## 快速启动（本机 venv，一键）

`+"```"+`bash
bash install.sh                         # 自动建 venv + pip install
export LLM_API_KEY=your-anthropic-api-key
source .venv/bin/activate && python3 server.py
# → http://localhost:3000
`+"```"+`

## Docker 部署（一键）

`+"```"+`bash
LLM_API_KEY=your-key docker compose up --build
# → http://localhost:3000
`+"```"+`

## 文件说明

- system-prompt.md — LLM 的 system prompt（排障知识合并）
- skills/ — 路由映射表 + 各 skill 的 SKILL.md
- scripts/ — 辅助脚本（resolve_runtime 等）
- server.py — Flask 聊天服务（调 Claude API）
- index.html — 聊天前端
- requirements.txt — Python 依赖清单
- Dockerfile — 容器镜像定义
- docker-compose.yaml — 容器化部署
- install.sh — 本机一键安装脚本
`, ctx.System.Name)
}
