package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PreloadConsul 通过 Consul HTTP API 列 kv。addr 形如 "http://consul:8500",token 可选。
//
// Consul 没有原生 namespace(开源版),我们把 **kv 根下的 top-level prefix** 当作 "namespace",
// 让每个 env 选一个 prefix(如 config / config-dev / config-prod 等),prefix 下的
// key 作为 dataId 供服务映射 —— 跟 nacos 的 (namespace × dataId) 语义对齐。
//
// 两阶段模式:
//
//	a) NamespacesOnly=true:GET /v1/kv/?keys=true&separator=/ 只列根下 top-level prefix,
//	   返回 Namespaces(不列具体 key),给 UI 下拉挑"这个 env 用哪个 prefix"。
//	b) 正常:req.Namespace = 选中的 prefix → GET /v1/kv/<prefix>?recurse=true&keys=true
//	   列该 prefix 下所有 key 作 Entries。
func PreloadConsul(req Request) (*Result, error) {
	addr := strings.TrimSpace(req.Addr)
	if addr == "" {
		return nil, fmt.Errorf("consul: addr 必填")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")

	httpCli := &http.Client{Timeout: 10 * time.Second}
	do := func(u string) ([]byte, int, error) {
		r, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, 0, err
		}
		if req.Token != "" {
			r.Header.Set("X-Consul-Token", req.Token)
		}
		resp, err := httpCli.Do(r)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b, resp.StatusCode, nil
	}

	// ── 先列 top-level prefix(两阶段都需要)──
	nsList, nsNotes := listConsulTopPrefixes(do, addr)

	// NamespacesOnly:到此为止,只返回 prefix 列表
	if req.NamespacesOnly {
		return &Result{
			Type:       "consul",
			Namespaces: nsList,
			Notes: append(nsNotes,
				fmt.Sprintf("只列了 kv 根下 %d 个 top-level prefix,未拉 keys", len(nsList))),
		}, nil
	}

	prefix := strings.Trim(strings.TrimSpace(req.Namespace), "/")
	if prefix == "" {
		prefix = "config" // 惯例默认
	}

	u := fmt.Sprintf("%s/v1/kv/%s?recurse=true&keys=true", addr, prefix)
	body, status, err := do(u)
	if err != nil {
		return nil, fmt.Errorf("连 %s 失败: %w(检查 consul 地址 / 端口 / ACL)", addr, err)
	}
	notes := append([]string{}, nsNotes...)

	if status == 404 {
		// consul: prefix 下无 key → 404。不视为错,返空结果 + 提示 + 仍带 namespaces 下拉
		notes = append(notes, fmt.Sprintf("prefix %q 下无 key(404);确认 prefix 拼写或先在 consul 写入 KV", prefix))
		return &Result{Type: "consul", Entries: nil, Namespaces: nsList, Notes: notes}, nil
	}
	if status == 401 || status == 403 {
		return nil, fmt.Errorf("consul ACL 拒绝访问(status=%d,prefix=%s):%s\n检查 X-Consul-Token 是否有 kv:read 权限",
			status, prefix, snippet(body))
	}
	if status != 200 {
		return nil, fmt.Errorf("list kv status %d: %s", status, snippet(body))
	}

	var keys []string
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, fmt.Errorf("decode kv keys: %w(body: %s)", err, snippet(body))
	}
	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		if strings.HasSuffix(k, "/") {
			continue // 目录节点,跳过
		}
		out = append(out, Entry{Locator: k, Tenant: prefix})
	}
	notes = append(notes, fmt.Sprintf("prefix=%s 共 %d 个 key", prefix, len(out)))
	return &Result{Type: "consul", Entries: out, Namespaces: nsList, Notes: notes}, nil
}

// listConsulTopPrefixes 列 kv 根下所有 top-level prefix (第一段路径)。
// 用 Consul 的 ?separator=/ 参数:把 recursive 探测截到第一层。失败时返空列表 + 一条 note,
// 不抛错(没权限列根时,用户可以直接手填 prefix)。
func listConsulTopPrefixes(
	do func(string) ([]byte, int, error),
	addr string,
) ([]Namespace, []string) {
	u := addr + "/v1/kv/?keys=true&separator=/"
	body, status, err := do(u)
	if err != nil {
		return nil, []string{fmt.Sprintf("⚠ 列根 kv 失败: %v", err)}
	}
	if status == 404 {
		return nil, []string{"⚠ consul kv 根为空(404),没 prefix 可选"}
	}
	if status == 401 || status == 403 {
		return nil, []string{fmt.Sprintf("⚠ token 无列根 kv 权限(status=%d),改手填 prefix", status)}
	}
	if status != 200 {
		return nil, []string{fmt.Sprintf("⚠ 列根 kv status=%d: %s", status, snippet(body))}
	}
	var keys []string
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, []string{fmt.Sprintf("⚠ 根 kv JSON 解析失败: %v", err)}
	}
	out := make([]Namespace, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		// "config/" → "config";跳过空
		trimmed := strings.TrimSuffix(k, "/")
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, Namespace{ID: trimmed, ShowName: trimmed})
	}
	return out, nil
}
