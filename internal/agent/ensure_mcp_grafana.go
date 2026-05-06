// ensure_mcp_grafana.go —— grafana/loki MCP 走的 go 二进制下载 + 装载逻辑。
//
// 为什么不用 npx -y @leval/mcp-grafana(原 staging 默认):
//   1. 该 npm 包启动时往 stdout 打 banner("Starting MCP Grafana server with stdio transport...")
//      污染 JSON-RPC 流,codex 握手解析第一帧不是合法 JSON 直接关 pipe → 表象是
//      "MCP startup failed: handshaking with MCP server failed: connection closed: initialize response",
//      次生 unhandled EPIPE 把整个 node 进程崩。
//   2. codex subagent thread 默认 network=Restricted,sandbox 内 npx 拉包可能 ENOTFOUND/EPERM。
//   3. node + @modelcontextprotocol/sdk 老版 stdio.js 不 catch socket write 错误,任何 stdout 关闭都崩。
//
// Go 版官方 mcp-grafana(github.com/grafana/mcp-grafana)三个问题全绕开:
//   - banner 写 stderr;stdout 严格 JSON-RPC
//   - 无运行时拉包,装好就能跑(不依赖网络出站)
//   - 单进程二进制无 SDK 链路 EPIPE 风险
//
// 装载位置:<install_root>/bin/mcp-grafana(默认 ~/.codex/bin/mcp-grafana,跟 customRoot 联动)。
package agent

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MCPGrafanaPinnedVersion 锁的版本。upstream 升级 schema/CLI 时要在这里手动 bump 后重测。
// 不用 "latest" tag —— 用户不同时机装会装到不同版本,行为不一致难复现 bug。
const MCPGrafanaPinnedVersion = "v0.13.1"

// mcpGrafanaBinPath 返回 <root>/bin/mcp-grafana 的绝对路径(install / uninstall 都用同一函数)。
func mcpGrafanaBinPath(root string) string {
	return filepath.Join(root, "bin", "mcp-grafana")
}

// EnsureMCPGrafanaBinary 保证 <root>/bin/mcp-grafana 存在且可执行。
// 已存在(任何版本)→ 直接复用,不强制覆盖(避免每次 install 重下);不存在 → 按平台
// 拼 GitHub release URL 下载 tarball + 解压。失败返回 error,调用方决定是 fallback 到
// npx 还是中断装机。
//
// 不做 SHA256 校验:GitHub release 走 HTTPS,中间人风险已经被 TLS 卡住;再加校验值得不偿失
// (要么 hardcode 跟版本绑死,要么再发起一次请求拉 checksums.txt,工程量翻倍收益微小)。
func EnsureMCPGrafanaBinary(root string) (string, error) {
	dst := mcpGrafanaBinPath(root)
	// 简单校验:是 Mach-O / ELF 而不是空文件 / 0 字节。size > 1 MiB 就够说明是真二进制。
	// 不到阈值的残文件后面 OpenFile(O_TRUNC) 会自动覆盖,无需先 Remove。
	if info, err := os.Stat(dst); err == nil && !info.IsDir() && info.Size() > 1<<20 {
		return dst, nil
	}

	platform, arch, err := mcpGrafanaPlatformAsset()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf(
		"https://github.com/grafana/mcp-grafana/releases/download/%s/mcp-grafana_%s_%s.tar.gz",
		MCPGrafanaPinnedVersion, platform, arch,
	)

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	if err := downloadAndExtractMCPGrafana(url, dst); err != nil {
		return "", fmt.Errorf("download mcp-grafana from %s: %w", url, err)
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return "", fmt.Errorf("chmod %s: %w", dst, err)
	}
	return dst, nil
}

// mcpGrafanaPlatformAsset 把 runtime.GOOS / GOARCH 映射成 grafana release 的命名约定。
//
//	GOOS=darwin   → Darwin
//	GOOS=linux    → Linux
//	GOOS=windows  → Windows(本工程不主动支持,但保留以防万一)
//	GOARCH=amd64  → x86_64
//	GOARCH=arm64  → arm64
//	GOARCH=386    → i386
func mcpGrafanaPlatformAsset() (platform, arch string, err error) {
	switch runtime.GOOS {
	case "darwin":
		platform = "Darwin"
	case "linux":
		platform = "Linux"
	case "windows":
		platform = "Windows"
	default:
		return "", "", fmt.Errorf("unsupported GOOS %q for mcp-grafana auto-download", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	case "386":
		arch = "i386"
	default:
		return "", "", fmt.Errorf("unsupported GOARCH %q for mcp-grafana auto-download", runtime.GOARCH)
	}
	return platform, arch, nil
}

// mcpGrafanaMaxBinarySize 解压时单文件写入上限(防恶意 mirror 给个 zip-bomb / 巨型 tarball
// 把磁盘写满)。当前 v0.13.1 的 mcp-grafana 二进制 ~30 MiB,留 5 倍裕度到 200 MiB。
const mcpGrafanaMaxBinarySize = 200 << 20

// downloadAndExtractMCPGrafana 拉 tarball + 解压找出 "mcp-grafana" 二进制写到 dst。
// 不写到磁盘 tmp 文件:tarball 才十几 MB,直接 stream 处理省一次磁盘往返。
func downloadAndExtractMCPGrafana(url, dst string) error {
	resp, err := http.Get(url) //nolint:gosec // URL 在调用方已经按 hardcoded version + arch 拼好,无注入风险
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("mcp-grafana binary not found in tarball")
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		// release tarball 里二进制名固定 "mcp-grafana"(macOS/Linux);windows 是 "mcp-grafana.exe"
		base := filepath.Base(hdr.Name)
		if base != "mcp-grafana" && base != "mcp-grafana.exe" {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		// LimitReader 防 mirror 返回巨型流灌爆磁盘;copied <= mcpGrafanaMaxBinarySize。
		copied, copyErr := io.Copy(out, io.LimitReader(tr, mcpGrafanaMaxBinarySize))
		_ = out.Close()
		if copyErr != nil {
			return copyErr
		}
		if copied >= mcpGrafanaMaxBinarySize {
			return fmt.Errorf("mcp-grafana binary exceeds %d-byte safety cap", mcpGrafanaMaxBinarySize)
		}
		return nil
	}
}

// MCPGrafanaInstallHint 在 ensure 失败时给用户的手装指引(install 报错 / 文档都用)。
// 注:这里展示版本是 codex 当前装机锁的,跟 latest 不一定一致(防版本漂移)。
func MCPGrafanaInstallHint(root string) string {
	platform, arch, _ := mcpGrafanaPlatformAsset()
	url := fmt.Sprintf(
		"https://github.com/grafana/mcp-grafana/releases/download/%s/mcp-grafana_%s_%s.tar.gz",
		MCPGrafanaPinnedVersion, platform, arch,
	)
	dst := mcpGrafanaBinPath(root)
	var sb strings.Builder
	fmt.Fprintf(&sb, "请手动装 mcp-grafana 二进制到 %s:\n", dst)
	fmt.Fprintf(&sb, "  mkdir -p %s\n", filepath.Dir(dst))
	fmt.Fprintf(&sb, "  curl -fsSL %s | tar -xz -C %s mcp-grafana\n", url, filepath.Dir(dst))
	fmt.Fprintf(&sb, "  chmod +x %s\n", dst)
	return sb.String()
}
