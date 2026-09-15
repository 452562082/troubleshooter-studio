// Package platform defines the platforms supported by Studio.
package platform

import (
	"os"
	"path/filepath"
)

func Supported(target string) bool {
	switch target {
	case "claude-code", "cursor", "codex", "opencode":
		return true
	}
	return false
}

// OpenCodeRoot follows OpenCode's XDG configuration directory.
func OpenCodeRoot(home string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "opencode")
}
