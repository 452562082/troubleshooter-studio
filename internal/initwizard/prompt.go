package initwizard

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Wizard 封装问答原语：所有交互都走 in/out，便于测试注入脚本
type Wizard struct {
	in  *bufio.Reader
	out io.Writer
}

func New(in io.Reader, out io.Writer) *Wizard {
	return &Wizard{in: bufio.NewReader(in), out: out}
}

func (w *Wizard) printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

// ask 读一行；用户回车空行 → 返回 defaultVal
func (w *Wizard) ask(label, defaultVal string) (string, error) {
	if defaultVal != "" {
		w.printf("  %s [%s]: ", label, defaultVal)
	} else {
		w.printf("  %s: ", label)
	}
	line, err := w.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return defaultVal, nil
	}
	return s, nil
}

// askBool: [Y/n] 默认 true，[y/N] 默认 false
func (w *Wizard) askBool(label string, defaultYes bool) (bool, error) {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	w.printf("  %s %s: ", label, hint)
	line, err := w.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return defaultYes, err
	}
	s := strings.ToLower(strings.TrimSpace(line))
	if s == "" {
		return defaultYes, nil
	}
	return s == "y" || s == "yes", nil
}

// askChoice: 选项中选一个，不匹配则重问一次；第二次仍不匹配则用 defaultVal
func (w *Wizard) askChoice(label string, choices []string, defaultVal string) (string, error) {
	hint := strings.Join(choices, "/")
	for attempt := 0; attempt < 2; attempt++ {
		s, err := w.ask(fmt.Sprintf("%s (%s)", label, hint), defaultVal)
		if err != nil {
			return "", err
		}
		for _, c := range choices {
			if s == c {
				return s, nil
			}
		}
		w.printf("    ! 无效选项 %q，请在 %s 中选\n", s, hint)
	}
	return defaultVal, nil
}

// section 打印分段标题
func (w *Wizard) section(title string) {
	w.printf("\n== %s ==\n", title)
}
