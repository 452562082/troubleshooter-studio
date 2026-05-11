package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PreloadApollo 通过 Apollo Open API 拉 env / app / cluster / namespace 列表。
// token(Authorization header)是必需的;addr 是 Portal 地址(Open API 所在),不是 meta-server。
//
// 跟 Nacos 的语义对齐:把 **Apollo env + cluster** 视为 Nacos 的 namespace,
// app 下的 **namespace(配置分组)** 视为 dataId。所以 `req.Namespace` = Apollo env 名
// (DEV / FAT / UAT / PRO,Portal 自定义也支持),`Entry.Locator` = namespace 名,
// `Entry.Group` = cluster,`Entry.AppID` = appID。
//
// 两阶段模式:
//
//	a) NamespacesOnly=true:列 Apollo envs(通过 meta-servers 接口 + fallback 常用 envs),
//	   返回 Namespaces(不拉 clusters / namespaces),UI 挑"这个 env.id 用哪个 Apollo env"。
//	b) 正常:req.Namespace = 选中的 Apollo env(如 "DEV"),req.AppID = 指定 app →
//	   列该 app 在该 env 下所有 cluster × namespace 作 Entries。
//	   req.AppID 留空 → 退化为列所有 app(老行为,当发现模式)。
func PreloadApollo(req Request) (*Result, error) {
	addr := strings.TrimSpace(req.Addr)
	if addr == "" {
		return nil, fmt.Errorf("apollo: addr(Portal URL)必填")
	}
	if req.Token == "" {
		return nil, fmt.Errorf("apollo: Open API token 必填")
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
		r.Header.Set("Authorization", req.Token)
		resp, err := httpCli.Do(r)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b, resp.StatusCode, nil
	}

	// ── 先列 Apollo envs(两阶段都需要作下拉) ──
	nsList, nsNotes := listApolloEnvs(do, addr)

	if req.NamespacesOnly {
		return &Result{
			Type:       "apollo",
			Namespaces: nsList,
			Notes: append(nsNotes,
				fmt.Sprintf("只列了 %d 个 Apollo env,未拉具体 namespaces", len(nsList))),
		}, nil
	}

	env := strings.TrimSpace(req.Namespace)
	if env == "" {
		env = "DEV" // Apollo 约定:DEV / FAT / UAT / PRO
	}

	if req.AppID != "" {
		// 单 app:列 clusters → 列 namespaces
		u := fmt.Sprintf("%s/openapi/v1/envs/%s/apps/%s/clusters", addr, env, req.AppID)
		body, status, err := do(u)
		if err != nil {
			return nil, fmt.Errorf("连 %s 失败: %w(检查 Portal 地址 / 网络)", addr, err)
		}
		if status == 401 || status == 403 {
			return nil, fmt.Errorf("apollo token 无权限(status=%d,env=%s app=%s):%s\n在 Portal 给 token 授予该 env+app 的访问权",
				status, env, req.AppID, snippet(body))
		}
		if status == 404 {
			return nil, fmt.Errorf("apollo 找不到 env=%s / app=%s(status=404),确认 env 名与 app_id 拼写", env, req.AppID)
		}
		if status != 200 {
			return nil, fmt.Errorf("list clusters status=%d: %s", status, snippet(body))
		}
		var clusters []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &clusters); err != nil {
			return nil, fmt.Errorf("decode clusters: %w(body: %s)", err, snippet(body))
		}
		var out []Entry
		notes := append([]string{}, nsNotes...)
		notes = append(notes, fmt.Sprintf("env=%s app=%s 共 %d clusters", env, req.AppID, len(clusters)))
		for _, c := range clusters {
			u2 := fmt.Sprintf("%s/openapi/v1/envs/%s/apps/%s/clusters/%s/namespaces",
				addr, env, req.AppID, c.Name)
			body2, status2, err := do(u2)
			if err != nil || status2 != 200 {
				notes = append(notes, fmt.Sprintf("⚠ cluster %s 列 ns 失败: status=%d %s", c.Name, status2, snippet(body2)))
				continue
			}
			var namespaces []struct {
				NamespaceName string `json:"namespaceName"`
				Format        string `json:"format"`
			}
			if err := json.Unmarshal(body2, &namespaces); err != nil {
				notes = append(notes, fmt.Sprintf("⚠ cluster %s ns JSON 解析失败: %v", c.Name, err))
				continue
			}
			for _, ns := range namespaces {
				out = append(out, Entry{
					Locator: ns.NamespaceName,
					Group:   c.Name, // 借用 Group 字段表 cluster
					Tenant:  env,    // entry.tenant = env(跟 Nacos 的 show_name 一致:前端按 env 过滤用)
					Type:    ns.Format,
					AppID:   req.AppID,
				})
			}
		}
		return &Result{Type: "apollo", Entries: out, Namespaces: nsList, Notes: notes}, nil
	}

	// 全量 app(AppID 留空:给用户展示有哪些 app;用户再回去填 AppID 精确拉)
	u := fmt.Sprintf("%s/openapi/v1/apps", addr)
	body, status, err := do(u)
	if err != nil {
		return nil, fmt.Errorf("连 %s 失败: %w", addr, err)
	}
	if status == 401 || status == 403 {
		return nil, fmt.Errorf("apollo token 无权限(status=%d):%s\n在 Portal 给 token 授予列 app 的权限",
			status, snippet(body))
	}
	if status != 200 {
		return nil, fmt.Errorf("list apps status=%d: %s", status, snippet(body))
	}
	var apps []struct {
		AppID string `json:"appId"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(body, &apps); err != nil {
		return nil, fmt.Errorf("decode apps: %w(body: %s)", err, snippet(body))
	}
	var out []Entry
	for _, a := range apps {
		out = append(out, Entry{
			Locator: a.Name,
			AppID:   a.AppID,
			Tenant:  env,
		})
	}
	notes := append([]string{}, nsNotes...)
	notes = append(notes, fmt.Sprintf("拉到 %d 个 app(列表级别);指定 app_id 再请求能看到具体 clusters / namespaces", len(apps)))
	return &Result{Type: "apollo", Entries: out, Namespaces: nsList, Notes: notes}, nil
}

// listApolloEnvs 列出 Portal 可用的 env 名。Apollo Open API 没直接的"列 envs"端点,
// 我们:
//  1. 先尝试 /openapi/v1/envs(部分新版 Portal 有,返 string 数组)
//  2. 回退 /openapi/v1/envclusters(同样部分版本有)
//  3. 都不支持 → 回退硬编码常用列表 DEV / FAT / UAT / PRO,外加 note 提示用户手改
//
// 返 (namespaces, notes);notes 里会说明数据来源,方便排查。
func listApolloEnvs(
	do func(string) ([]byte, int, error),
	addr string,
) ([]Namespace, []string) {
	// 尝试 /openapi/v1/envs (string 数组)
	body, status, err := do(addr + "/openapi/v1/envs")
	if err == nil && status == 200 {
		var envs []string
		if jerr := json.Unmarshal(body, &envs); jerr == nil && len(envs) > 0 {
			out := make([]Namespace, 0, len(envs))
			for _, e := range envs {
				out = append(out, Namespace{ID: e, ShowName: e})
			}
			return out, []string{fmt.Sprintf("从 /openapi/v1/envs 拿到 %d 个 env", len(envs))}
		}
	}
	// 硬编码 fallback
	defaults := []string{"DEV", "FAT", "UAT", "PRO"}
	out := make([]Namespace, 0, len(defaults))
	for _, e := range defaults {
		out = append(out, Namespace{ID: e, ShowName: e})
	}
	return out, []string{
		"Apollo Open API 不支持列 envs,用常用 [DEV / FAT / UAT / PRO] 作选项;如 Portal 自定义 env 名请手改 yaml",
	}
}
