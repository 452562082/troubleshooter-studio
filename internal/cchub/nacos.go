// nacos.go —— Nacos 配置中心 Preload 入口 + 客户端类型 + 共享 snippet helper。
//
// Nacos 两个大版本 API 完全不同,我们探测后自动适配:
//
//	v1 (Nacos 1.x / 2.x,兼容层在 3.x 仍保留但不推荐)
//	  probe:  GET  <ctx>/v1/console/server/state
//	  login:  POST <ctx>/v1/auth/login       → {accessToken}
//	  list:   GET  <ctx>/v1/cs/configs       → {totalCount, pageItems:[{dataId,group,tenant,type}]}
//
//	v3 (Nacos 3.x,2025-11 之后常见的新部署)
//	  probe:  GET  <ctx>/v3/console/server/state
//	  login:  POST <ctx>/v3/auth/user/login  → {accessToken}(跟 v1 结构基本一致)
//	  list:   GET  <ctx>/v3/console/cs/config/list
//	           → {code,message,data:{totalCount,pageItems:[{dataId,groupName,namespaceId,type}]}}
//
// contextPath:
//
//	"/nacos" - 官方 docker / war 默认
//	""       - K8s Ingress 剥前缀 / 阿里云 MSE / 某些裸 jar 部署
//
// 组合起来 4 种可能:{/nacos v3} / {/nacos v1} / {"" v3} / {"" v1},探测时逐个 GET
// server/state,哪个 200 用哪个。
//
// 子文件:
//
//	nacos_probe.go   probeFlavor / probeDashboard / login(接入握手三件套)
//	nacos_list.go    nsInfo + listNamespaces + listConfigs + fetchConfigsPage
//	fetch_nacos.go   connect/fetchOne(给 FetchContent 用,Preload 链路无关)
package cchub

import (
	"fmt"
	"net/http"
	"strings"
)

type apiFlavor struct {
	ContextPath string
	Version     string // "v1" | "v3"
}

// nacosClient 一个带连接池的客户端(connpool.go 维护共享实例)。
type nacosClient struct {
	base      string // "http://<host>:<port>"
	flavor    apiFlavor
	probeNote string // probeFlavor 返回的人话信息(e.g. "检测到 Nacos:API=v3,根路径部署")
	httpCli   *http.Client
	username  string
	password  string
	token     string // login 后缓存,连接池内可跨请求复用
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

// snippet 跨文件共享 —— probe / list / fetch 的所有错误消息都用它截 body。
func snippet(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return strings.ReplaceAll(string(b), "\n", " ")
}
