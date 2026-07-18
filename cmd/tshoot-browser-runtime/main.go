// Command tshoot-browser-runtime prepares the pinned Chromium runtime used by
// desktop release packaging. It is a build helper, not an end-user workflow.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/browserverify"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func main() {
	root := flag.String("root", "", "isolated management root for the prepared browser runtime")
	playwrightCache := flag.String("playwright-cache", "", "optional existing Playwright browser cache used as a verified seed")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "--root is required")
		os.Exit(2)
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve runtime root: %v\n", err)
		os.Exit(1)
	}
	manager := browserverify.NewRuntimeManager(absolute, nil)
	if *playwrightCache != "" {
		manager.SetPlaywrightBrowserCache(*playwrightCache)
	} else if userCache, cacheErr := os.UserCacheDir(); cacheErr == nil {
		candidate := filepath.Join(userCache, "ms-playwright")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			manager.SetPlaywrightBrowserCache(candidate)
		}
	}
	paths, err := manager.Ensure(context.Background(), func(progress bughub.BrowserProgress) {
		if progress.Total > 0 {
			fmt.Fprintf(os.Stderr, "[browser-runtime] %s %d/%d\n", progress.Code, progress.Current, progress.Total)
			return
		}
		fmt.Fprintf(os.Stderr, "[browser-runtime] %s\n", progress.Code)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare browser runtime: %v\n", err)
		os.Exit(1)
	}
	// stdout is intentionally machine-readable for the Makefile command
	// substitution; progress stays on stderr.
	fmt.Println(paths.Root)
}
