// dependency_scan.go —— 跨语言扫"本仓库调了哪些下游服务 / 用了哪些数据层"。
//
// 设计:简单 regex 扫,**不是**完整 AST 分析。准确率目标 50-70% 而非 100%——
// 即使扫漏一半,生成的 service-dependency-map.yaml 比 100% 占位空白强 10 倍。
// 用户拿到种子值改比从空白起强 10 倍。
//
// 各语言模式:
//   - Go    : http.Get/Post/Do(URL) / grpc.Dial("host:port") / mongo.Connect / redis.NewClient
//   - Java  : @FeignClient(name="...") / RestTemplate.exchange / WebClient.create / @Autowired Redis/Mongo
//   - Python: requests.get/post(URL) / pymongo.MongoClient / redis.Redis / aiohttp.ClientSession
//   - Node  : axios.get/post(URL) / fetch(URL) / mongoose.connect / new Redis(...)
//
// 输出 RepoAnalysis 的 DownstreamCalls + DataStoreUsages 字段。
//
// 各 stack 实现已按域拆到子文件:
//
//	dependency_scan_go.go      Go(含 services.go 常量映射两遍扫)
//	dependency_scan_java.go    Java/Kotlin(@FeignClient + Spring Data 模板)
//	dependency_scan_python.go  Python(requests / pymongo / sqlalchemy)
//	dependency_scan_node.go    Node/TS(axios / mongoose / typeorm)
package analyzer

import (
	"context"
	"net/url"
	"strings"
)

// ScanDependencies 给定 repoPath,扫所有 stack 适用文件,产出 downstream calls + data store usages。
// includePaths 跟 walker 一样的过滤语义。
func ScanDependencies(stack, repoPath string, includePaths []string) (calls []DownstreamCall, usages []DataStoreUsage) {
	return ScanDependenciesContext(context.Background(), stack, repoPath, includePaths)
}

func ScanDependenciesContext(ctx context.Context, stack, repoPath string, includePaths []string) (calls []DownstreamCall, usages []DataStoreUsage) {
	switch stack {
	case "go":
		return scanGoDeps(ctx, repoPath, includePaths)
	case "java":
		return scanJavaDeps(ctx, repoPath, includePaths)
	case "python":
		return scanPythonDeps(ctx, repoPath, includePaths)
	case "node":
		return scanNodeDeps(ctx, repoPath, includePaths)
	default:
		return nil, nil
	}
}

// targetFromURL 从 URL 提目标服务名:host 部分按 - / . 切片去 env 后缀,留主体。
// 例:
//
//	"http://user-service-dev.svc:8080/api/v1/users" → "user-service"
//	"https://payment.example.com/notify"           → "payment"
//	"user-prod:50051" → "user"
func targetFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// 不带 scheme 时 url.Parse 会把整串当 Path
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" || host == "localhost" || host == "127.0.0.1" {
		return ""
	}
	// 取第一段(K8s service 名通常是 host 的第一段),去掉 -dev/-prod/-staging/-test 后缀
	first := strings.SplitN(host, ".", 2)[0]
	for _, suf := range []string{"-dev", "-prod", "-staging", "-stg", "-test", "-uat", "-pre"} {
		first = strings.TrimSuffix(first, suf)
	}
	return first
}

// dedupeCalls 同 (target, driver) 去重,保留第一次出现的 callsite。
func dedupeCalls(in []DownstreamCall) []DownstreamCall {
	seen := map[string]bool{}
	out := make([]DownstreamCall, 0, len(in))
	for _, c := range in {
		if c.Target == "" {
			continue
		}
		key := c.Target + "|" + c.Driver
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func dedupeUsages(in []DataStoreUsage) []DataStoreUsage {
	seen := map[string]bool{}
	out := make([]DataStoreUsage, 0, len(in))
	for _, u := range in {
		if u.Type == "" {
			continue
		}
		key := u.Type + "|" + u.Logical + "|" + u.Driver
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, u)
	}
	return out
}

// camelToKebab 把 CamelCase / PascalCase 转 kebab-case。给 NewUserClient → "user"、
// NewOrderRpcClient → "order-rpc"、NewUgcClient → "ugc" 这种 fallback 命名用。
// 全大写片段(连续大写)整体保留为小写不拆分,避免 "URL" → "u-r-l" 这种烂结果。
func camelToKebab(s string) string {
	if s == "" {
		return ""
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			// 前面有字母,且 (前一个是小写 或 当前是大写片段最后一个+下一个是小写) → 加 '-'
			if i > 0 {
				prev := s[i-1]
				prevLower := prev >= 'a' && prev <= 'z'
				next := byte(0)
				if i+1 < len(s) {
					next = s[i+1]
				}
				nextLower := next >= 'a' && next <= 'z'
				prevUpper := prev >= 'A' && prev <= 'Z'
				if prevLower || (prevUpper && nextLower) {
					b = append(b, '-')
				}
			}
			b = append(b, c+('a'-'A'))
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}
