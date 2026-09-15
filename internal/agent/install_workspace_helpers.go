// 安装共用的目录复制工具。
package agent

import (
	"io/fs"
	"os"
	"path/filepath"
)

func copyDirAll(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if installShouldSkipGeneratedArtifact(d.Name()) {
				return fs.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if installShouldSkipGeneratedArtifact(d.Name()) {
			return nil
		}
		if err := copyFileSimple(p, target); err != nil {
			return err
		}
		// 保留 exec 位:Linux 上某些 skill 脚本要可执行
		if info, err := d.Info(); err == nil {
			_ = os.Chmod(target, info.Mode())
		}
		return nil
	})
}
