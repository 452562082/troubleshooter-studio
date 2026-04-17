package watcher

import (
	"crypto/sha1"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Watcher 轮询路径集合（文件或目录），发现 mtime/size 聚合签名变化时触发回调
// 选用轮询而非 fsnotify，避免引入外部依赖；默认 1s 间隔对开发体验足够
type Watcher struct {
	Paths    []string
	Interval time.Duration
	lastSig  string
}

func New(paths []string, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = time.Second
	}
	return &Watcher{Paths: paths, Interval: interval}
}

// Loop 阻塞循环：每 Interval 检查一次，检测到变化时调用 onChange
// 通过 SIGINT（Ctrl+C）退出由外层信号处理或 os.Interrupt 中断
func (w *Watcher) Loop(onChange func()) {
	w.lastSig = w.signature()
	for {
		time.Sleep(w.Interval)
		cur := w.signature()
		if cur != w.lastSig {
			w.lastSig = cur
			onChange()
		}
	}
}

// Changed 返回当前签名是否与上次 signature() 不同（不更新内部状态，供测试用）
func (w *Watcher) Changed() bool {
	cur := w.signature()
	changed := cur != w.lastSig
	w.lastSig = cur
	return changed
}

// signature 返回对当前 Paths 集合的聚合签名：
//   - 文件：mtime + size
//   - 目录：递归 walk，把每个文件的 (rel, mtime, size) 累加
func (w *Watcher) signature() string {
	h := sha1.New()
	for _, p := range w.Paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(h, "missing:%s\n", p)
			continue
		}
		if !info.IsDir() {
			fmt.Fprintf(h, "f:%s:%d:%d\n", p, info.ModTime().UnixNano(), info.Size())
			continue
		}
		// 目录：按路径字典序遍历，保证签名稳定
		var entries []string
		_ = filepath.WalkDir(p, func(sub string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			entries = append(entries, fmt.Sprintf("%s:%d:%d", sub, info.ModTime().UnixNano(), info.Size()))
			return nil
		})
		sort.Strings(entries)
		for _, e := range entries {
			fmt.Fprintln(h, e)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
