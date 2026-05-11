// nacos_list.go —— Nacos 列 namespace + 列 configs 实现(v1/v3 双版本兼容)。
// PreloadNacos 三种模式(NamespacesOnly / 单 ns 精确 / 全 ns 枚举)都依赖本文件的两个 list*。
package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

// nsInfo:内部用的 namespace 元信息。ID 给 API 调用,ShowName 给 UI 展示。
type nsInfo struct {
	ID       string // UUID,public 为空串
	ShowName string // 友好名,UI 展示 / 聚合时标识来源
}

// listNamespaces 列 nacos 里所有 namespace。v1/v3 路径不同,返回结构也不同。
//
// v3: GET /v3/console/core/namespace/list
//
//	响应 {"code":0,"data":[{"namespace":"<uuid>","namespaceShowName":"<friendly>","configCount":N,"type":N}]}
//
// v1: GET /v1/console/namespaces
//
//	响应 {"code":200,"data":[{"namespace":"<uuid>","namespaceShowName":"<friendly>",...}]}
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
		GroupName   string `json:"groupName"` // v3 字段名
		Tenant      string `json:"tenant"`
		NamespaceID string `json:"namespaceId"` // v3 字段名
		Type        string `json:"type"`
	}
	var entries []Entry
	totalCount, pagesAvail := 0, 0
	if c.flavor.Version == "v3" {
		var doc struct {
			Code int `json:"code"`
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
