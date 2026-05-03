// role_hint_node.go —— Node 仓库角色推断:读 package.json deps。
//   含前端框架(react/vue/next/vite/angular/svelte)且不含后端框架 → frontend
//   含后端框架(express/koa/nestjs/fastify/hapi/midway/egg)→ backend
//   仅 lib/util 风格(无 main + 仅 export)→ common-lib
//   含 react-native / expo / capacitor / nativescript → mobile
package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func roleFromNode(repoPath string) RoleHint {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return RoleHint{}
	}
	var pkg struct {
		Name            string            `json:"name"`
		Main            string            `json:"main"`
		Bin             json.RawMessage   `json:"bin"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Scripts         map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return RoleHint{}
	}
	allDeps := map[string]bool{}
	for k := range pkg.Dependencies {
		allDeps[k] = true
	}
	for k := range pkg.DevDependencies {
		allDeps[k] = true
	}

	frontFrameworks := []string{"react", "vue", "next", "nuxt", "vite", "angular", "@angular/core", "svelte", "preact", "solid-js", "remix"}
	backFrameworks := []string{"express", "koa", "@nestjs/core", "fastify", "@hapi/hapi", "midway", "egg", "@feathersjs/feathers"}
	mobileMarkers := []string{"react-native", "expo", "ionic", "@capacitor/core", "nativescript"}

	matchAny := func(list []string) string {
		for _, k := range list {
			if allDeps[k] {
				return k
			}
		}
		return ""
	}

	if hit := matchAny(mobileMarkers); hit != "" {
		return RoleHint{Role: "mobile", Reason: "package.json 含 " + hit}
	}

	frontHit := matchAny(frontFrameworks)
	backHit := matchAny(backFrameworks)
	if frontHit != "" && backHit == "" {
		return RoleHint{Role: "frontend", Reason: "package.json 含 " + frontHit + ",无后端框架"}
	}
	if backHit != "" && frontHit == "" {
		return RoleHint{Role: "backend", Reason: "package.json 含 " + backHit}
	}
	if frontHit != "" && backHit != "" {
		// 同时有 — 多半是 SSR(next/nuxt 自带 server),按 frontend 划分(BFF/SSR)
		return RoleHint{Role: "frontend", Reason: "package.json 同时含 " + frontHit + " + " + backHit + "(SSR / BFF)"}
	}
	// 没框架 + 看 main / bin —— 像 common-lib
	if pkg.Main != "" && len(pkg.Bin) == 0 && len(pkg.Scripts["start"]) == 0 {
		return RoleHint{Role: "common-lib", Reason: "package.json 有 main 但无 bin / 无 start 脚本"}
	}
	return RoleHint{}
}
