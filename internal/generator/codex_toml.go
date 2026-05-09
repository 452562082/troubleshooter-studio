package generator

import (
	"fmt"
	"strings"
)

// codex agent toml 里 generator 写、install 时替换的占位 sentinel。
// 写读两侧共用同一常量,避免改名时一边漏改导致静默 desync。
const (
	// CodexPlaceholderSkillsRoot 在 staging agents/<name>.toml 里 [[skills.config]].path 占位;
	// install 替换为 <root>/skills/<name> 绝对路径(codex 不解析 ~ / $HOME)。
	CodexPlaceholderSkillsRoot = "{{SKILLS_ROOT}}"
	// CodexMCPRegionBegin / CodexMCPRegionEnd 是 install 时 [mcp_servers.*] 段的 idempotent
	// region marker。staging toml 末尾写空 region(begin + end 两行紧邻,中间无内容),install
	// 用 begin..end(含两行)整体替换 → 重装幂等。用户手改 toml 时只要保留这两行就能继续
	// 重装;若 marker 都被删,install 会报错而不是默默拼到末尾(避免无限堆叠)。
	CodexMCPRegionBegin = "# >>> tshoot mcp begin (managed; do not edit between markers)"
	CodexMCPRegionEnd   = "# <<< tshoot mcp end"
)

// codex_toml.go —— 给 codex agent toml 写值用的最小 TOML helper。
//
// 故意不引第三方 TOML 库:agent toml schema 简单(顶层 string + multi-line + 内联 table),
// 自己写 escape + emit 反而稳定;引库会拖一坨依赖只为 marshal 这一处。
//
// 限制:
//   - 仅支持 string / multi-line string / inline-table 三种;不支持嵌套 array of tables 等
//   - escape 严格按 TOML 规范:`\` `"` 转义;control chars (U+0000..U+001F 除 tab/lf) 用 \uXXXX
//   - multi-line literal string ('''...''') 不用,因为 codex 例子用的是 multi-line basic string ("""...""")

// TomlString 把任意字符串编码成 TOML basic string 字面量(单行,带双引号)。
// 单行字符串里换行 \n / 制表符 \t 等都会被转义。
func TomlString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				sb.WriteString(fmt.Sprintf(`\u%04X`, r))
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// TomlWriteString 写一行 `key = "value"`。
func TomlWriteString(sb *strings.Builder, key, value string) {
	sb.WriteString(key)
	sb.WriteString(" = ")
	sb.WriteString(TomlString(value))
	sb.WriteString("\n")
}

