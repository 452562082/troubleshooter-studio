package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectGoFramework 覆盖新规则的关键点:
//   - indirect 依赖不算
//   - substring 不误判(github.com/grpc-ecosystem/grpc-gateway 不应被识别为 grpc
//     除非项目本身还直接 require google.golang.org/grpc)
//   - 完整框架(kratos/go-zero)比 http router(gin)优先
//   - gin + grpc 共存,选 gin(HTTP router 比 RPC-only 精确)
//   - 只有 grpc 直接 require,才标 grpc(truss 这种情况)
//   - 大版本后缀(/v2)要被前缀命中
func TestDetectGoFramework(t *testing.T) {
	cases := []struct {
		name   string
		goMod  string
		expect string
	}{
		{
			name: "plain grpc project (truss-like)",
			goMod: `module example.com/truss
require (
    google.golang.org/grpc v1.77.0
    github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.3 // indirect
    github.com/joho/godotenv v1.5.1
)`,
			expect: "grpc",
		},
		{
			name: "only grpc-gateway as indirect should NOT trigger grpc",
			goMod: `module example.com/foo
require github.com/joho/godotenv v1.5.1
require github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.3 // indirect`,
			expect: "",
		},
		{
			name: "kratos beats gin when both present",
			goMod: `module example.com/foo
require (
    github.com/go-kratos/kratos/v2 v2.7.0
    github.com/gin-gonic/gin v1.9.0
)`,
			expect: "kratos",
		},
		{
			name: "gin beats grpc (http router before rpc-only)",
			goMod: `module example.com/foo
require (
    github.com/gin-gonic/gin v1.9.0
    google.golang.org/grpc v1.77.0
)`,
			expect: "gin",
		},
		{
			name: "indirect gin does not count",
			goMod: `module example.com/foo
require (
    github.com/foo/bar v1.0.0
    github.com/gin-gonic/gin v1.9.0 // indirect
)`,
			expect: "",
		},
		{
			name:   "empty go.mod",
			goMod:  `module example.com/foo`,
			expect: "",
		},
		{
			name: "go-zero identified",
			goMod: `module example.com/foo
require github.com/zeromicro/go-zero v1.6.0`,
			expect: "go-zero",
		},
		{
			name: "kitex identified",
			goMod: `module example.com/foo
require github.com/cloudwego/kitex v0.8.0`,
			expect: "kitex",
		},
		{
			name: "hertz identified",
			goMod: `module example.com/foo
require github.com/cloudwego/hertz v0.8.0`,
			expect: "hertz",
		},
		{
			name: "goframe identified",
			goMod: `module example.com/foo
require github.com/gogf/gf/v2 v2.5.0`,
			expect: "goframe",
		},
		{
			name: "replace block ignored",
			goMod: `module example.com/foo
require github.com/joho/godotenv v1.5.1
replace (
    github.com/go-kratos/kratos/v2 => ./local-kratos
)`,
			expect: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.goMod), 0o644); err != nil {
				t.Fatal(err)
			}
			got := detectGoFramework(dir)
			if got != tc.expect {
				t.Errorf("detectGoFramework() = %q, want %q\ngo.mod:\n%s",
					got, tc.expect, tc.goMod)
			}
		})
	}
}
