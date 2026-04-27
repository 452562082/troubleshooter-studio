package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Nacos 两个大版本 API 完全不同,我们探测后自动适配:
//
//   v1 (Nacos 1.x / 2.x,兼容层在 3.x 仍保留但不推荐)
//     probe:  GET  <ctx>/v1/console/server/state
//     login:  POST <ctx>/v1/auth/login       → {accessToken}
//     list:   GET  <ctx>/v1/cs/configs       → {totalCount, pageItems:[{dataId,group,tenant,type}]}
//
//   v3 (Nacos 3.x,2025-11 之后常见的新部署)
//     probe:  GET  <ctx>/v3/console/server/state
//     login:  POST <ctx>/v3/auth/user/login  → {accessToken}(跟 v1 结构基本一致)
//     list:   GET  <ctx>/v3/console/cs/config/list
//              → {code,message,data:{totalCount,pageItems:[{dataId,groupName,namespaceId,type}]}}
//
// contextPath:
//   "/nacos" - 官方 docker / war 默认
//   ""       - K8s Ingress 剥前缀 / 阿里云 MSE / 某些裸 jar 部署
//
// 组合起来 4 种可能:{/nacos v3} / {/nacos v1} / {"" v3} / {"" v1},探测时逐个 GET
// server/state,哪个 200 用哪个。

type apiFlavor struct {
	ContextPath string
	Version     string // "v1" | "v3"
}

// nacosClient 一个带连接池的客户端(connpool.go 维护共享实例)。
type nacosClient struct {
	base       string // "http://<host>:<port>"
	flavor     apiFlavor
	probeNote  string // probeFlavor 返回的人话信息(e.g. "检测到 Nacos:API=v3,根路径部署")
	httpCli    *http.Client
	username   string
	password   string
	token      string // login 后缓存,连接池内可跨请求复用
}

func PreloadNacos(req Request) (*Result, error) {
	// 连接池复用:同 (addr,user,pass) 30 分钟内共享 probe + login 结果,
	// 避免 Step 5 两阶段(NamespacesOnly + 精确拉) / Step 7 batch 各自重复 probe+login。
	cli, err := getOrConnectNacos(req.Addr, req.Username, req.Password)
	if err != nil {
		return nil, err
	}
	notes := []string{}
	if cli.probeNote != "" {
		notes = append(notes, cli.probeNote)
	}

	// 3) 两种模式:
	//    a) NamespacesOnly=true:只列 namespaces,不拉 configs。用于向导"第一步探测":
	//       前端拿到 namespace 列表后按 env.id 启发式匹,再发第二次精确请求。快,不浪费。
	//    b) 正常拉取:
	//       - req.Namespace 非空 → 只查这一个 namespace(用户挑好了)
	//       - req.Namespace 空 → 枚举全部 namespace(发现模式;保留给直接调 API 的场景)
	var allEntries []Entry
	reqNS := strings.TrimSpace(req.Namespace)

	if req.NamespacesOnly {
		namespaces, err := cli.listNamespaces()
		if err != nil {
			return nil, fmt.Errorf("列 namespace 失败: %w", err)
		}
		nsList := make([]Namespace, 0, len(namespaces))
		for _, n := range namespaces {
			nsList = append(nsList, Namespace{ID: n.ID, ShowName: n.ShowName})
		}
		notes = append(notes, fmt.Sprintf("只列了 namespace(共 %d 个),未拉 configs", len(nsList)))
		return &Result{Type: "nacos", Entries: nil, Namespaces: nsList, Notes: notes}, nil
	}

	if reqNS != "" {
		entries, listNotes, err := cli.listConfigs(reqNS)
		if err != nil {
			return nil, err
		}
		// 即使只查一个 namespace,也把那条 namespace 的 show_name 挂到 entry.Tenant
		// 让前端 filter-by-namespace 时能对上(前端按 entry.tenant === selectedNsShowName 过滤)。
		if nss, err := cli.listNamespaces(); err == nil {
			for _, ns := range nss {
				if ns.ID == reqNS {
					for i := range entries {
						entries[i].Tenant = ns.ShowName
					}
					break
				}
			}
		}
		allEntries = entries
		notes = append(notes, listNotes...)
	} else {
		namespaces, err := cli.listNamespaces()
		if err != nil {
			// 列 namespace 失败:降级到只查默认 public,给个提示
			notes = append(notes, fmt.Sprintf("⚠ 列 namespace 失败,仅查 public: %v", err))
			entries, listNotes, e2 := cli.listConfigs("")
			if e2 != nil {
				return nil, e2
			}
			allEntries = entries
			notes = append(notes, listNotes...)
		} else {
			notes = append(notes, fmt.Sprintf("扫描了 %d 个 namespace", len(namespaces)))
			for _, ns := range namespaces {
				entries, _, err := cli.listConfigs(ns.ID)
				if err != nil {
					notes = append(notes, fmt.Sprintf("⚠ namespace %s(%s)拉取失败: %v", ns.ShowName, ns.ID, err))
					continue
				}
				// 把友好 namespace 名字挂到 Entry.Tenant,UI 展示 chip 时就能知道来自哪个 ns
				for i := range entries {
					entries[i].Tenant = ns.ShowName
				}
				allEntries = append(allEntries, entries...)
			}
		}
	}

	// 把内部 nsInfo 转成公开 Namespace(ID+ShowName),给 UI 下拉用。
	// 任何非 NamespacesOnly 的调用也顺带把 namespaces 带回去,让前端多一个来源可用。
	nsList := []Namespace{}
	if namespaces, err := cli.listNamespaces(); err == nil {
		for _, n := range namespaces {
			nsList = append(nsList, Namespace{ID: n.ID, ShowName: n.ShowName})
		}
	}

	return &Result{Type: "nacos", Entries: allEntries, Namespaces: nsList, Notes: notes}, nil
}

// nsInfo:内部用的 namespace 元信息。ID 给 API 调用,ShowName 给 UI 展示。
type nsInfo struct {
	ID       string // UUID,public 为空串
	ShowName string // 友好名,UI 展示 / 聚合时标识来源
}

// listNamespaces 列 nacos 里所有 namespace。v1/v3 路径不同,返回结构也不同。
//
// v3: GET /v3/console/core/namespace/list
//     响应 {"code":0,"data":[{"namespace":"<uuid>","namespaceShowName":"<friendly>","configCount":N,"type":N}]}
// v1: GET /v1/console/namespaces
//     响应 {"code":200,"data":[{"namespace":"<uuid>","namespaceShowName":"<friendly>",...}]}
//
// 两者都把 "public" 作为第一个默认条目(namespace 为空串);我们统一处理成 ns.ID="" for public。
func (c *nacosClient) listNamespaces() ([]nsInfo, error) {
	var u string
	if c.flavor.Version == "v3" {
		u = c.base + c.flavor.ContextPath + "/v3/console/core/namespace/list"
	} else {
		u = c.base + c.flavor.ContextPath + "/v1/console/namespaces"
	}
	if c.token != "" {
		u += "?accessToken=" + url.QueryEscape(c.token)
	}
	resp, err := c.httpCli.Get(u)
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("list namespaces status %d: %s", resp.StatusCode, snippet(body))
	}
	// 两个版本 data 结构基本一致,都是 [{namespace, namespaceShowName, ...}]
	type rawNS struct {
		Namespace         string `json:"namespace"`
		NamespaceShowName string `json:"namespaceShowName"`
	}
	var doc struct {
		Code int     `json:"code"`
		Data []rawNS `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode ns list: %w(body: %s)", err, snippet(body))
	}
	out := make([]nsInfo, 0, len(doc.Data))
	for _, r := range doc.Data {
		name := r.NamespaceShowName
		if name == "" {
			name = r.Namespace
		}
		if name == "" {
			name = "public"
		}
		out = append(out, nsInfo{ID: r.Namespace, ShowName: name})
	}
	return out, nil
}

// probeFlavor 按 **v3 优先** 的顺序试路径,首个能确认是 Nacos(200 / 401 / 403 / 410)
// 的就用;v1 只作为对老版本部署(Nacos 2.x)的后备 —— Nacos 3.x 默认已禁用 v1 console API,
// 命中 v1 会返 410 提示 "请用 v3",我们会据此直接跳回 v3 + /nacos,不白试后续 candidate。
//
// 返 (flavor, human-note, err)。note 会展示到日志,方便用户确认"实际用了哪个版本"。
func (c *nacosClient) probeFlavor() (apiFlavor, string, error) {
	// v3 优先(2025+ 主流),其中 /nacos 前缀放前面(docker 默认),根路径放后面(ingress 改写场景)
	// v1 兜底(只给 Nacos 2.x / 1.x 用;3.x 命中 v1 会返 410,我们会据此切回 v3)
	candidates := []apiFlavor{
		{"/nacos", "v3"}, // Nacos 3.x 主路径
		{"", "v3"},       // Nacos 3.x 根路径部署
		{"/nacos", "v1"}, // Nacos 2.x / 1.x 后备
		{"", "v1"},       // Nacos 2.x / 1.x 根路径后备
	}
	// 记录每一个尝试的结果,失败时全部抛到错误消息里,用户一眼看清每条路径的响应
	var attempts []string
	for _, f := range candidates {
		u := c.base + f.ContextPath + "/" + f.Version + "/console/server/state"
		resp, err := c.httpCli.Get(u)
		if err != nil {
			// 网络不可达(dial timeout / DNS 解析失败),所有 candidate 都会 fail,直接返
			return apiFlavor{}, "", fmt.Errorf("连 %s 失败: %w(检查网络 / 地址 / 端口 / VPC 是否可达)", c.base, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case 200:
			note := fmt.Sprintf("检测到 Nacos:API=%s", f.Version)
			if f.ContextPath == "" {
				note += ",根路径部署(无 /nacos 前缀)"
			}
			return f, note, nil
		case 401, 403:
			// 路径对但要 auth → 仍当命中,登录流程会继续 POST 账号密码。
			// Nacos 3.x 某些部署 /v3/console/server/state 直接要登录,才能避免走到 v1 踩 410。
			note := fmt.Sprintf("检测到 Nacos:API=%s(需登录才能访问 console/state)", f.Version)
			if f.ContextPath == "" {
				note += ",根路径部署"
			}
			return f, note, nil
		case 410:
			// v1 console API 已被 3.x 禁用;响应体会提示 "please use ${contextPath:nacos}/v3/..."
			// 直接按 v3 + /nacos 走,跳过剩余 candidate 不再白试
			bodyStr := string(body)
			if strings.Contains(bodyStr, "/v3/") || strings.Contains(bodyStr, "contextPath:nacos") {
				return apiFlavor{ContextPath: "/nacos", Version: "v3"},
					"检测到 Nacos 3.x:v1 console API 已禁用,自动切换到 /nacos/v3 路径", nil
			}
			attempts = append(attempts, fmt.Sprintf("  GET %s → 410 Gone: %s", u, snippet(body)))
		default:
			attempts = append(attempts, fmt.Sprintf("  GET %s → %d %s", u, resp.StatusCode, snippet(body)))
		}
	}
	// 路径都非 200,但 base 通 → 再试一下"/"或"/nacos/"是不是 dashboard(HTML),
	// 能帮用户识别是 Nacos 但 API 路径变了 vs 地址根本不是 Nacos
	dashHint := probeDashboard(c.httpCli, c.base)
	return apiFlavor{}, "", fmt.Errorf(
		"无法定位 %s 的 Nacos API(已按 v3 优先顺序探测 4 个路径,全部未识别)。\n"+
			"尝试结果:\n%s\n%s\n可能原因:\n"+
			"  1) 地址端口不是 Nacos(确认 %s 是 Nacos 服务端,非 dashboard 反代)\n"+
			"  2) Nacos 被反向代理挡了 → 试直连端口 8848\n"+
			"  3) 私网 IP 不可达(AWS 172.31.x.x / 阿里云 VPC 等 → 开 VPN/跳板)\n"+
			"  4) Nacos 3.x 魔改 context path(非 /nacos) → 联系运维查 server.servlet.context-path",
		c.base, strings.Join(attempts, "\n"), dashHint, c.base)
}

// probeDashboard 探测一下 "/" 和 "/nacos/" 是不是返回 HTML(Nacos dashboard),
// 如果是 → 说明地址确实是 Nacos 但 API 路径变了;如果不是 → 地址根本不对。
// 这条信息加到 probeFlavor 的错误消息里,帮用户排查。
func probeDashboard(cli *http.Client, base string) string {
	for _, p := range []string{"/", "/nacos/"} {
		resp, err := cli.Get(base + p)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 && strings.Contains(strings.ToLower(string(body)), "nacos") {
			return fmt.Sprintf("探测 %s%s 返回含 \"nacos\" 的 HTML → 地址是 Nacos dashboard 但 API 路径未识别,可能被 ingress 改写过", base, p)
		}
	}
	return "探测 / 与 /nacos/ 都没看到 Nacos dashboard 特征 → 地址可能根本不是 Nacos,或 ingress 把根页面换了"
}

// login v1/v3 路径不同,form body 格式相同(x-www-form-urlencoded)。
func (c *nacosClient) login() error {
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)
	var loginPath string
	switch c.flavor.Version {
	case "v3":
		loginPath = c.flavor.ContextPath + "/v3/auth/user/login"
	default:
		loginPath = c.flavor.ContextPath + "/v1/auth/login"
	}
	resp, err := c.httpCli.PostForm(c.base+loginPath, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, snippet(body))
	}
	var doc struct {
		AccessToken string `json:"accessToken"`
		TokenTTL    int    `json:"tokenTtl"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("decode login resp: %w(body: %s)", err, snippet(body))
	}
	if doc.AccessToken == "" {
		return fmt.Errorf("accessToken 空(账号或密码错?body: %s)", snippet(body))
	}
	c.token = doc.AccessToken
	return nil
}

// listConfigs 分页拉配置列表。v1/v3 路径 + response 结构不同,在内部分两分支。
func (c *nacosClient) listConfigs(namespace string) ([]Entry, []string, error) {
	const pageSize = 500
	const maxPages = 5

	var out []Entry
	notes := []string{}
	for page := 1; page <= maxPages; page++ {
		entries, totalCount, pagesAvail, err := c.fetchConfigsPage(namespace, page, pageSize)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, entries...)
		if page == 1 && totalCount > pageSize*maxPages {
			notes = append(notes, fmt.Sprintf("namespace 下共 %d 条,只拉了前 %d 条", totalCount, pageSize*maxPages))
		}
		if page >= pagesAvail {
			break
		}
	}
	if len(out) == 0 {
		notes = append(notes, fmt.Sprintf("namespace=%q 下没有配置", namespace))
	}
	return out, notes, nil
}

func (c *nacosClient) fetchConfigsPage(namespace string, page, pageSize int) ([]Entry, int, int, error) {
	q := url.Values{}
	q.Set("pageNo", fmt.Sprintf("%d", page))
	q.Set("pageSize", fmt.Sprintf("%d", pageSize))
	if c.token != "" {
		q.Set("accessToken", c.token)
	}

	var u string
	if c.flavor.Version == "v3" {
		// v3:namespaceId 参数名;console/cs/config/list 路径
		q.Set("namespaceId", namespace)
		u = c.base + c.flavor.ContextPath + "/v3/console/cs/config/list?" + q.Encode()
	} else {
		// v1:tenant 参数名;cs/configs 路径;需 search=accurate + dataId/group 占位
		q.Set("search", "accurate")
		q.Set("dataId", "")
		q.Set("group", "")
		if namespace != "" {
			q.Set("tenant", namespace)
		}
		u = c.base + c.flavor.ContextPath + "/v1/cs/configs?" + q.Encode()
	}

	resp, err := c.httpCli.Get(u)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list configs: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 0, 0, fmt.Errorf("list configs status %d: %s", resp.StatusCode, snippet(body))
	}

	// v3:{"code":0,"message":"success","data":{"totalCount":N,"pageItems":[{dataId,groupName,namespaceId,type}]}}
	// v1:{"totalCount":N,"pageItems":[{dataId,group,tenant,type}]}
	type pageItem struct {
		DataID      string `json:"dataId"`
		Group       string `json:"group"`
		GroupName   string `json:"groupName"`   // v3 字段名
		Tenant      string `json:"tenant"`
		NamespaceID string `json:"namespaceId"` // v3 字段名
		Type        string `json:"type"`
	}
	var entries []Entry
	totalCount, pagesAvail := 0, 0
	if c.flavor.Version == "v3" {
		var doc struct {
			Code int    `json:"code"`
			Data struct {
				TotalCount     int        `json:"totalCount"`
				PagesAvailable int        `json:"pagesAvailable"`
				PageItems      []pageItem `json:"pageItems"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, 0, 0, fmt.Errorf("decode v3 resp: %w(body: %s)", err, snippet(body))
		}
		totalCount, pagesAvail = doc.Data.TotalCount, doc.Data.PagesAvailable
		for _, it := range doc.Data.PageItems {
			g := it.Group
			if g == "" {
				g = it.GroupName
			}
			t := it.Tenant
			if t == "" {
				t = it.NamespaceID
			}
			entries = append(entries, Entry{Locator: it.DataID, Group: g, Tenant: t, Type: it.Type})
		}
	} else {
		var doc struct {
			TotalCount     int        `json:"totalCount"`
			PagesAvailable int        `json:"pagesAvailable"`
			PageItems      []pageItem `json:"pageItems"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, 0, 0, fmt.Errorf("decode v1 resp: %w(body: %s)", err, snippet(body))
		}
		totalCount, pagesAvail = doc.TotalCount, doc.PagesAvailable
		for _, it := range doc.PageItems {
			entries = append(entries, Entry{Locator: it.DataID, Group: it.Group, Tenant: it.Tenant, Type: it.Type})
		}
	}
	return entries, totalCount, pagesAvail, nil
}

func snippet(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return strings.ReplaceAll(string(b), "\n", " ")
}
