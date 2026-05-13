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
//  1. PATH 有 `kafka-mcp-server` → 返回 LookPath 绝对路径
//  2. cache `~/.tshoot/bin/kafka-mcp-server-<ver>` 已存在且可执行 → 复用(下次部署免下载)
//  3. 否则下载 GitHub Release tarball 解到 `~/.tshoot/bin/kafka-mcp-server-<ver>` → 用绝对路径
//  4. 下载失败 → warn 给手动安装指引,本次 install 不阻塞(kafka MCP 会启动失败,
//     但其它 MCP 不受影响)
//
// 文件名带 `<ver>` 后缀:bump kafkaMCPVersion 后旧文件自动 cache miss 触发重下,避免静默用旧版。
// 老版本 binary 留在 ~/.tshoot/bin/ 等定期手动清(参考早期 mcp-grafana 孤儿 binary 教训)。
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
	"strconv"
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

// EnsureKafkaMCPInstalled 保证 kafka-mcp-server binary 在本机可执行,返回**绝对路径**给
// buildKafka 写进 ~/.claude.json 的 command 字段。
//
// 关键:返回**绝对路径**(不是 "kafka-mcp-server" 字面),因为 Claude Code MCP 子进程的 PATH
// 跟 install 时跑的 shell PATH 不一样 —— mac 桌面 app 启动子进程的 PATH 来自 launchd GUI 默认,
// 只有 /usr/bin:/bin:/usr/sbin:/sbin,brew prefix(/opt/homebrew/bin)被 strip。install 看到
// PATH 有 binary 写字面 "kafka-mcp-server",Claude Code 启动时同名找不到 ENOENT 静默挂掉。
// 这跟 findOpenclawCLI(install_native_openclaw.go,commit e44c74d)修过的是同一个坑。
//
// 返回 (binPath, err):
//   - PATH 命中 → (LookPath 的绝对路径, nil)
//   - cache 命中 → (~/.tshoot/bin/kafka-mcp-server-<ver>, nil)
//   - 下载成功 → 同上
//   - 失败 → ("", err)。调用方打 warn 给手动指引,buildKafka 回落字面 "kafka-mcp-server"(用户
//     事后手动装到 PATH 也能直接生效不需要重跑 install)
//
// onLog(line):流式日志回调(nil 跳过),给 desktop / CLI 进度展示用。
func EnsureKafkaMCPInstalled(onLog func(string)) (string, error) {
	log := onLog
	if log == nil {
		log = func(string) {}
	}
	// 1) PATH 探测 — 返回 LookPath 的绝对路径(p),不是 "kafka-mcp-server" 字面,见函数注释。
	if p, err := exec.LookPath("kafka-mcp-server"); err == nil {
		log(fmt.Sprintf("[ok] kafka-mcp-server 已在 PATH:%s", p))
		return p, nil
	}
	// Windows 没实现 zip 自动解压,直接走手动安装路径 — 避免误导用户"自动下载中"然后失败。
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("Windows 不支持自动下载 kafka-mcp-server,请手动安装:\n%s", kafkaMCPInstallHint())
	}
	// 2) 本机 cache 命中。文件名带版本号 — bump kafkaMCPVersion 后旧文件不被命中触发重下,
	// 老 binary 留 ~/.tshoot/bin/ 等定期清(避免 mcp-grafana 早期那种"换策略后孤儿 binary"坑)。
	cachePath, err := kafkaMCPCachePath()
	if err != nil {
		return "", err
	}
	// 任意 execute bit(0o111)— umask 异常时也能识别已装好的 binary,不为了 owner-only 的吹毛求疵
	// 强制重下。
	if fi, err := os.Stat(cachePath); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
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

// kafkaMCPCachePath 返回固定 cache 路径 ~/.tshoot/bin/kafka-mcp-server-<ver>[.exe]。
// 跟 ~/.openclaw 平级,跨 IDE 共享一份 binary,卸载 IDE 不会误删。
// 文件名带版本号:升级 kafkaMCPVersion 后旧文件 cache miss 自动重下,避免静默用旧版。
func kafkaMCPCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := "kafka-mcp-server-" + kafkaMCPVersion
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, ".tshoot", "bin", name), nil
}

// maxKafkaMCPDownloadBytes 限制下载体积上限,防恶意 mirror 重定向到 /dev/urandom 灌爆磁盘。
// tuannvm v2.0.2 实际 tarball ~5 MiB,200 MiB 留十倍空间应对上游加码 binary 也够用。
const maxKafkaMCPDownloadBytes = 200 << 20

// downloadKafkaMCPBinary 从 GitHub Release 拉 tarball 解到 dest。
//
// URL 模板(参考 release HTML 实际命名,小写 OS):
//
//	kafka-mcp-server_<ver-no-v>_<os>_<arch>.tar.gz  (darwin/linux,Windows zip 走 EnsureKafkaMCPInstalled 上层提前 return)
//
// 内部 tarball 结构:CHANGELOG.md + README.md + kafka-mcp-server。
// 只挑 binary 这一个文件写出去,其它忽略。提取后做 Content-Length 校验防中途截断
// 写出短 binary 导致下次 cache 命中误用。
//
// dest 的 basename 是 "kafka-mcp-server-<version>",但 tarball 内 binary 名是 "kafka-mcp-server"
// 不带版本,所以匹配走固定常量,不用 filepath.Base(dest)。
func downloadKafkaMCPBinary(dest string, log func(string)) error {
	osName := runtime.GOOS // darwin / linux
	arch := runtime.GOARCH // amd64 / arm64
	verNoV := strings.TrimPrefix(kafkaMCPVersion, "v")
	url := fmt.Sprintf(
		"https://github.com/tuannvm/kafka-mcp-server/releases/download/%s/kafka-mcp-server_%s_%s_%s.tar.gz",
		kafkaMCPVersion, verNoV, osName, arch,
	)

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
	// Body 包 LimitReader 防恶意 mirror 无限流量灌爆;tarball 才几 MiB,200 MiB 够 10× 余量。
	body := io.LimitReader(resp.Body, maxKafkaMCPDownloadBytes)

	gz, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("gzip reader:%w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	const binNameInTar = "kafka-mcp-server" // tarball 内名,不带版本号
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next:%w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binNameInTar {
			continue
		}
		// tmp 名加 pid 防并发 install:两个 IDE 同时跑 install 时不会用同一个 tmp 名互相覆盖。
		tmp := dest + ".tmp." + strconv.Itoa(os.Getpid())
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create %s:%w", tmp, err)
		}
		n, err := io.Copy(f, tr)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("copy:%w", err)
		}
		if err := f.Close(); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("close %s:%w", tmp, err)
		}
		// 校验 tar header 声明的 Size — 防 HTTP 中途截断后 io.Copy 静默返回 nil
		// (gzip 流被掐断会被 gzip.Reader 报错,但 tar 体被掐成短 binary 不一定能 caught;
		// 比 header.Size 短 = 截断,删 tmp 报错而不是 rename 上去当好的)。
		if hdr.Size > 0 && n != hdr.Size {
			os.Remove(tmp)
			return fmt.Errorf("truncated download:wrote %d bytes, tar header expects %d", n, hdr.Size)
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("rename:%w", err)
		}
		return nil
	}
	return fmt.Errorf("tarball 内没找到 %s 文件", binNameInTar)
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
