// bindings_one2all.go —— one2all-remote MCP 资源预加载 binding。
//
// 用途:Step 5/Step 8 选 one2all 时,用户填了 MCP URL + Bearer Token 后点"加载资源",
// 走 MCP JSON-RPC 协议调 one2all 的 platform_list_clusters / platform_list_namespaces
// / platform_list_deployments 等工具,把集群/namespace/Deployment 树拉回来,UI 渲染成下拉。
//
// one2all MCP 返回规范:
//   - HTTP 响应体是 SSE 格式: event: message\ndata: <JSON-RPC>\n\n
//   - JSON-RPC result.content[0].text 是工具返回的 JSON 字符串
//   - 所有工具返回的 JSON 都是 {"data": <数组或对象>} 格式
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/dsprobe"
)

// ── 对外类型(Wails 绑定用) ──────────────────────────────────────────

type One2AllClusterEntry struct {
	Name       string           `json:"name"`
	ClusterID  string           `json:"cluster_id"` // 转为字符串,方便 YAML
	Namespaces []One2AllNsEntry `json:"namespaces"`
}

type One2AllNsEntry struct {
	Name       string   `json:"name"`
	ConfigMaps []string `json:"configmaps,omitempty"`
}

type One2AllDeploymentEntry struct {
	Name     string `json:"name"`
	Selector string `json:"selector,omitempty"`
}

type One2AllResources struct {
	Clusters []One2AllClusterEntry `json:"clusters"`
	Notes    []string              `json:"notes,omitempty"`
}

type One2AllDeployments struct {
	Deployments []One2AllDeploymentEntry `json:"deployments"`
}

// ── MCP JSON-RPC 通信 ───────────────────────────────────────────────

// one2allMCPCall 调用 one2all MCP 工具,解析返回的 JSON-RPC 响应(支持 SSE)。
// result 必须是指针,工具返回的 data 字段会 Unmarshal 进去。
func one2allMCPCall(mcpURL, token, toolName string, args map[string]any, result any) error {
	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
		"id": 1,
	}
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")

	client := &http.Client{
		Timeout: 30 * time.Second,
		// 带 Bearer token 出站 → 默认校验证书防 MITM 偷 token;内网自签用
		// TSHOOT_INSECURE_TLS=1 放行(见 dsprobe.TLSConfigForProbe)。
		Transport: &http.Transport{
			TLSClientConfig: dsprobe.TLSConfigForProbe(true),
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MCP returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
	}

	// SSE 格式(event:/data:) → 提取 data: 行的 JSON
	jsonBody := respBody
	if bytes.HasPrefix(respBody, []byte("event:")) || bytes.HasPrefix(respBody, []byte("data:")) {
		for _, line := range bytes.Split(respBody, []byte{'\n'}) {
			t := bytes.TrimSpace(line)
			if bytes.HasPrefix(t, []byte("data:")) {
				jsonBody = bytes.TrimSpace(t[5:])
				break
			}
		}
	}

	// 解析 JSON-RPC 响应
	var rpcResp struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(jsonBody, &rpcResp); err != nil {
		return fmt.Errorf("parse rpc response: %w (body: %s)", err, string(jsonBody[:min(len(jsonBody), 300)]))
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("MCP error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if rpcResp.Result == nil || len(rpcResp.Result.Content) == 0 {
		return fmt.Errorf("MCP returned empty result")
	}

	// 工具返回的 JSON 字符串在 content[0].text 里。
	// one2all 的所有工具返回格式都是 {"data": <数组或对象>}
	text := rpcResp.Result.Content[0].Text
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err != nil {
		return fmt.Errorf("parse tool result wrapper: %w (text: %s)", err, text[:min(len(text), 300)])
	}
	if err := json.Unmarshal(wrapper.Data, result); err != nil {
		return fmt.Errorf("parse tool result data: %w (data: %s)", err, string(wrapper.Data[:min(len(wrapper.Data), 300)]))
	}
	return nil
}

// ── 内部辅助类型(MCP 返回的原始形状) ─────────────────────────────────

type o2aClusterRaw struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type o2aNSRaw struct {
	Name string `json:"name"`
}

type o2aConfigMapRaw struct {
	Name string `json:"name"`
}

type o2aDeployRaw struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// ── App 方法 ─────────────────────────────────────────────────────────

// One2AllListResources 调 one2all MCP 拉集群→namespace 树。
func (a *App) One2AllListResources(mcpURL, token string, includeConfigMaps bool) (*One2AllResources, error) {
	res := &One2AllResources{}

	// 1. 列集群
	var clusters []o2aClusterRaw
	if err := one2allMCPCall(mcpURL, token, "platform_list_clusters", nil, &clusters); err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}

	for _, c := range clusters {
		entry := One2AllClusterEntry{
			Name:      c.Name,
			ClusterID: strconv.Itoa(c.ID),
		}

		// 2. 列 namespace
		var namespaces []o2aNSRaw
		nsArgs := map[string]any{"cluster_id": c.ID}
		if err := one2allMCPCall(mcpURL, token, "platform_list_namespaces", nsArgs, &namespaces); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("集群 %s(%d) 列 namespace 失败: %v", c.Name, c.ID, err))
			res.Clusters = append(res.Clusters, entry)
			continue
		}

		for _, ns := range namespaces {
			nsEntry := One2AllNsEntry{Name: ns.Name}

			// 3. 列 ConfigMap
			if includeConfigMaps {
				var configMaps []o2aConfigMapRaw
				cmArgs := map[string]any{"cluster_id": fmt.Sprintf("%d", c.ID), "namespace": ns.Name}
				if err := one2allMCPCall(mcpURL, token, "platform_list_configmaps", cmArgs, &configMaps); err != nil {
					res.Notes = append(res.Notes, fmt.Sprintf("集群 %s/%s 列 ConfigMap 失败: %v", c.Name, ns.Name, err))
				} else {
					for _, cm := range configMaps {
						nsEntry.ConfigMaps = append(nsEntry.ConfigMaps, cm.Name)
					}
				}
			}

			entry.Namespaces = append(entry.Namespaces, nsEntry)
		}
		res.Clusters = append(res.Clusters, entry)
	}

	return res, nil
}

// One2AllListDeployments 拉指定 cluster+namespace 下的 Deployment 列表。
func (a *App) One2AllListDeployments(mcpURL, token, clusterID, namespace string) (*One2AllDeployments, error) {
	res := &One2AllDeployments{}

	// cluster_id 参数:one2all API 接受 int,我们传字符串让它自动转
	cid, err := strconv.Atoi(clusterID)
	if err != nil {
		cid = 0 // fallback
	}
	args := map[string]any{
		"cluster_id": cid,
		"namespace":  namespace,
	}

	var deployments []o2aDeployRaw
	if err := one2allMCPCall(mcpURL, token, "platform_list_deployments", args, &deployments); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}

	for _, d := range deployments {
		selector := ""
		if app, ok := d.Labels["app"]; ok {
			selector = "app=" + app
		}
		res.Deployments = append(res.Deployments, One2AllDeploymentEntry{
			Name:     d.Name,
			Selector: selector,
		})
	}
	return res, nil
}

// One2AllConfigMapEntry 批量读取的一个 ConfigMap 定位
type One2AllConfigMapEntry struct {
	ClusterID string `json:"cluster_id"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// One2AllConfigMapResult 单个 ConfigMap 的读取结果
type One2AllConfigMapResult struct {
	ClusterID string `json:"cluster_id"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Content   string `json:"content"` // 整个 data 字段的 JSON(keys → values)
	Error     string `json:"error,omitempty"`
}

// One2AllFetchConfigMaps 批量读取 ConfigMap 内容,用于数据层自动识别。
// 对每个 ConfigMap 调 platform_get_configmap 拿 data 字段,转成 JSON 字符串返回。
func (a *App) One2AllFetchConfigMaps(mcpURL, token string, configs []One2AllConfigMapEntry) []One2AllConfigMapResult {
	var results []One2AllConfigMapResult
	for _, cfg := range configs {
		result := One2AllConfigMapResult{
			ClusterID: cfg.ClusterID,
			Namespace: cfg.Namespace,
			Name:      cfg.Name,
		}
		args := map[string]any{
			"cluster_id": cfg.ClusterID,
			"namespace":  cfg.Namespace,
			"name":       cfg.Name,
		}
		// platform_get_configmap 返回 {"namespace":"..","name":"..","keys":[...],"data":{...}}
		var cmData struct {
			Keys []string          `json:"keys"`
			Data map[string]string `json:"data"`
		}
		if err := one2allMCPCall(mcpURL, token, "platform_get_configmap", args, &cmData); err != nil {
			result.Error = err.Error()
		} else {
			content, _ := json.Marshal(cmData.Data)
			result.Content = string(content)
		}
		results = append(results, result)
	}
	return results
}
