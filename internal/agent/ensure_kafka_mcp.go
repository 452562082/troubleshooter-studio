// ensure_kafka_mcp.go —— 确保 `kafka-mcp-server` binary 可用。
//
// kafka 这家 MCP 业界没有靠谱的 npx/uvx 实现:
//   - npm 上唯一活跃的 `@confluentinc/mcp-confluent` 依赖 native librdkafka 绑定
//     (`@confluentinc/kafka-javascript`),prebuilt Node ABI 矩阵滞后(Node 26 没出 v147 prebuilt),
//     install scripts 静默失败导致 binding 缺失,跨平台脆弱(2026-05 实战踩坑确诊)
//   - PyPI 上 `kafka-mcp-server` 0.1.1 只 2 个工具 placeholder,生产不可用;其余 PyPI 候选
//     都依赖 `confluent-kafka` Python wrapper(同样 librdkafka)
//
// 所以 kafka 这家**走 binary 安装**(franz-go 纯 Go,无 native deps,跨平台 GoReleaser):
//   - 上游:tuannvm/kafka-mcp-server(MIT,brew tap + GitHub Release 5 个 triple 全)
//   - 装一次永远稳,不受 Node / glibc / librdkafka 版本飘移影响
//
// 策略:跟 mcp-grafana 早期"自动下载"模式同源 —— kafka-mcp-server 是单一用途 binary
// (不是 uv 那种系统级工具),装哪儿不污染用户系统,适合 install 时**自动拉**。流程:
//
//  1. PATH 有 `kafka-mcp-server` → 直接用,什么都不做
//  2. 我们 cache 目录 `~/.tshoot/bin/kafka-mcp-server` 已存在 → 直接用(下次部署免下载)
//  3. 否则下载 GitHub Release tarball 解到 `~/.tshoot/bin/` → 用绝对路径
//  4. 下载失败 → warn 给手动安装指引,本次 install 不阻塞(kafka MCP 会启动失败,
//     但其它 MCP 不受影响)
package agent

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// kafkaMCPVersion 固定上游 release tag。不查 latest:
//   - 少一次网络调用(GitHub API 偶尔限流)
//   - 上游出大坑时不会自动跟着崩(明确升级走 PR + test)
// 升级 cadence:观察上游 ~1 季度同步一次。
const kafkaMCPVersion = "v2.0.2"

// CfgUsesKafkaMCP 判断 cfg 是否启用了 kafka 数据层。
// 用于决定要不要发 kafka-mcp-server 探测/安装动作 — 没启 kafka 就不用费事。
func CfgUsesKafkaMCP(cfg *config.SystemConfig) bool {
	for _, ds := range cfg.Infrastructure.DataStores {
		if ds.Enabled && ds.Type == "kafka" {
			return true
		}
	}
	return false
}

// EnsureKafkaMCPInstalled 保证 kafka-mcp-server binary 在本机可执行。
//
// 返回 (binPath, err):
//   - PATH 命中 → ("kafka-mcp-server", nil)。buildKafka 写 PATH 形式 command。
//   - cache 命中 → (绝对路径, nil)。buildKafka 写绝对路径。
//   - 下载成功 → (绝对路径, nil)。同上。
//   - 下载失败 → ("", err)。调用方打 warn,buildKafka 仍写 PATH 形式(用户后续手动装好后无需重跑 install)。
//
// onLog(line):流式日志回调(nil 跳过),给 desktop / CLI 进度展示用。
func EnsureKafkaMCPInstalled(onLog func(string)) (string, error) {
	log := onLog
	if log == nil {
		log = func(string) {}
	}
	// 1) PATH 探测
	if p, err := exec.LookPath("kafka-mcp-server"); err == nil {
		log(fmt.Sprintf("[ok] kafka-mcp-server 已在 PATH:%s", p))
		return "kafka-mcp-server", nil
	}
	// 2) 本机 cache 命中
	cachePath, err := kafkaMCPCachePath()
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(cachePath); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o100 != 0 {
		log(fmt.Sprintf("[ok] kafka-mcp-server 已在 cache:%s", cachePath))
		return cachePath, nil
	}
	// 3) 自动下载
	log(fmt.Sprintf("[info] kafka-mcp-server 未装,自动下载 %s → %s", kafkaMCPVersion, cachePath))
	if err := downloadKafkaMCPBinary(cachePath, log); err != nil {
		return "", fmt.Errorf("自动下载 kafka-mcp-server 失败:%w\n%s", err, kafkaMCPInstallHint())
	}
	log(fmt.Sprintf("[ok] kafka-mcp-server 下载完成:%s", cachePath))
	return cachePath, nil
}

// kafkaMCPCachePath 返回固定 cache 路径 ~/.tshoot/bin/kafka-mcp-server[.exe]。
// 跟 ~/.openclaw 平级,跨 IDE 共享一份 binary,卸载 IDE 不会误删。
func kafkaMCPCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := "kafka-mcp-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, ".tshoot", "bin", name), nil
}

// downloadKafkaMCPBinary 从 GitHub Release 拉 tarball/zip 解到 dest。
//
// URL 模板(参考 release HTML 实际命名,小写 OS):
//   kafka-mcp-server_<ver-no-v>_<os>_<arch>.tar.gz  (darwin/linux)
//   kafka-mcp-server_<ver-no-v>_<os>_<arch>.zip      (windows)
//
// 内部 tarball 结构:CHANGELOG.md + README.md + kafka-mcp-server[.exe]。
// 只挑 kafka-mcp-server[.exe] 这一个文件写出去,其它忽略。
func downloadKafkaMCPBinary(dest string, log func(string)) error {
	osName := runtime.GOOS // darwin / linux / windows
	arch := runtime.GOARCH // amd64 / arm64
	verNoV := strings.TrimPrefix(kafkaMCPVersion, "v")
	ext := "tar.gz"
	if osName == "windows" {
		ext = "zip" // tuannvm 上游 windows 走 zip
	}
	url := fmt.Sprintf(
		"https://github.com/tuannvm/kafka-mcp-server/releases/download/%s/kafka-mcp-server_%s_%s_%s.%s",
		kafkaMCPVersion, verNoV, osName, arch, ext,
	)
	if ext == "zip" {
		// Windows zip 解压需要 archive/zip,本会话 mac/linux 优先,Windows 用户走文档手动安装路径。
		// 真有需求再补;手动装 README 链接走 GitHub Release。
		return fmt.Errorf("Windows zip 自动解压未实现,请走手动安装:%s", url)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir:%w", err)
	}

	log(fmt.Sprintf("[info] 拉 tarball:%s", url))
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download:%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status=%d url=%s", resp.StatusCode, url)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip reader:%w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	binName := filepath.Base(dest)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next:%w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binName {
			continue
		}
		// 落盘走 tmp 再 rename,避免半写状态下次探测命中残文件。
		tmp := dest + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create %s:%w", tmp, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("copy:%w", err)
		}
		if err := f.Close(); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("close %s:%w", tmp, err)
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("rename:%w", err)
		}
		return nil
	}
	return fmt.Errorf("tarball 内没找到 %s 文件", binName)
}

// kafkaMCPInstallHint 给用户的手动装机指引(自动下载失败时的 fallback 文案)。
func kafkaMCPInstallHint() string {
	var sb strings.Builder
	sb.WriteString("kafka 数据层 MCP 走 `kafka-mcp-server` binary 启动(tuannvm/kafka-mcp-server,MIT,franz-go 纯 Go 无 native deps)。\n")
	sb.WriteString("自动下载失败,请手动装(任选其一):\n")
	switch runtime.GOOS {
	case "darwin":
		sb.WriteString("  brew install tuannvm/mcp/kafka-mcp-server\n")
		sb.WriteString("  # 或从 GitHub Release 直下 binary:\n")
		sb.WriteString("  # https://github.com/tuannvm/kafka-mcp-server/releases/latest\n")
	case "windows":
		sb.WriteString("  # 从 GitHub Release 下载 windows zip 包,解压放 PATH:\n")
		sb.WriteString("  # https://github.com/tuannvm/kafka-mcp-server/releases/latest\n")
	default: // linux + 其它 unix
		sb.WriteString("  # 从 GitHub Release 下载对应 arch 的 tarball,解压放 PATH:\n")
		sb.WriteString("  # https://github.com/tuannvm/kafka-mcp-server/releases/latest\n")
		sb.WriteString("  # brew 也可(Linuxbrew):brew install tuannvm/mcp/kafka-mcp-server\n")
	}
	sb.WriteString("装好后重跑 install,kafka-mcp-server 会被自动 LookPath 命中。\n")
	sb.WriteString("(其它 MCP 不依赖 kafka-mcp-server,本次 install 不阻塞继续装。)")
	return sb.String()
}
