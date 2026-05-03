// bindings_kuboard_workload.go —— Kuboard 运行时 Service / Deployment 查询 +
// 一站式 Pod 快照 binding。
//
// Service / Deployment 列举走 cluster-cache(Service: direct,Deployment: 分页);
// PodSnapshot 内部串调 KuboardListPods + KuboardListEvents + KuboardGetPodLogs(×2),
// 给 agent 一个开盒即用的"健康 + 根因方向"快照入口。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// KuboardServiceInfo Service + Endpoints 复合视图(用户最关心"后面挂了几个 pod")
type KuboardServiceInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	ClusterIP string            `json:"cluster_ip"`
	Type      string            `json:"type"`
	Ports     []string          `json:"ports"` // "tcp/8080" / "tcp/80→8080"
	Selector  map[string]string `json:"selector,omitempty"`
}

func (a *App) KuboardListServices(in KuboardListPodsInput) ([]KuboardServiceInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()
	raw, err := kuboardDirectGET(s, "resource=services&namespace="+url.QueryEscape(in.Namespace))
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			List []struct {
				Data struct {
					Metadata struct {
						Name, Namespace string
					} `json:"metadata"`
					Spec struct {
						Type      string            `json:"type"`
						ClusterIP string            `json:"clusterIP"`
						Selector  map[string]string `json:"selector"`
						Ports     []struct {
							Port       int    `json:"port"`
							TargetPort any    `json:"targetPort"`
							Protocol   string `json:"protocol"`
						} `json:"ports"`
					} `json:"spec"`
				} `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 services 失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]KuboardServiceInfo, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		svc := KuboardServiceInfo{
			Name: it.Data.Metadata.Name, Namespace: it.Data.Metadata.Namespace,
			ClusterIP: it.Data.Spec.ClusterIP, Type: it.Data.Spec.Type,
			Selector: it.Data.Spec.Selector,
		}
		for _, p := range it.Data.Spec.Ports {
			tag := strings.ToLower(p.Protocol) + "/" + fmt.Sprintf("%d", p.Port)
			if tp, ok := p.TargetPort.(float64); ok && int(tp) != p.Port {
				tag += fmt.Sprintf("→%d", int(tp))
			}
			svc.Ports = append(svc.Ports, tag)
		}
		out = append(out, svc)
	}
	return out, nil
}

// KuboardDeploymentInfo Deployment 精简视图,排障最关心 "在滚动吗 / 副本到位吗"
type KuboardDeploymentInfo struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	Replicas          int      `json:"replicas"`
	UpdatedReplicas   int      `json:"updated_replicas"`
	ReadyReplicas     int      `json:"ready_replicas"`
	AvailableReplicas int      `json:"available_replicas"`
	Strategy          string   `json:"strategy"`             // RollingUpdate / Recreate
	Conditions        []string `json:"conditions,omitempty"` // ["Available=True", "Progressing=True (ReplicaSetUpdated)"]
	// Selector 是 spec.selector.matchLabels 拼成 "k=v,k=v"。向导用它给 service_map 自动
	// 回填 label_selector,运行时排障时 routing skill 也直接读这个值给 KuboardListPods。
	Selector string `json:"selector,omitempty"`
}

func (a *App) KuboardListDeployments(in KuboardListPodsInput) ([]KuboardDeploymentInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()
	// Kuboard v4 列非 core 资源的正确端点(swagger 文档确认):
	//   GET /api/cluster.kuboard.cn/v4/cluster-cache
	//     ?pageNum=1&pageSize=N
	//     &apiGroup=apps                     # core 资源不传
	//     &resource=deployments
	//     &namespaced=true
	//     &clusterIdNamespaces=<uid>/<ns>    # 同时支持多个 = 跨 ns 查
	//     &orderBy=name
	// 注:不是 cluster-cache/direct(那是按 name 取单条) / 也不是 cluster-cache/list。
	hitURL := fmt.Sprintf("%s/api/cluster.kuboard.cn/v4/cluster-cache"+
		"?pageNum=1&pageSize=500&apiGroup=apps&resource=deployments&namespaced=true"+
		"&clusterIdNamespaces=%s%%2F%s&orderBy=name",
		s.base, url.QueryEscape(s.clusterUID), url.QueryEscape(in.Namespace))
	req, _ := http.NewRequestWithContext(s.ctx, http.MethodGet, hitURL, nil)
	req.Header.Set("Kb-Access-Key", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Kuboard 失败: %v;URL=%s", err, hitURL)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d;URL=%s;响应=%s", resp.StatusCode, hitURL, snippet(raw))
	}
	// 兼容两种 list item 形态:
	//   1) cluster-cache 分页接口:list[i] = { metadata, spec, status, ... }(平铺)
	//   2) cluster-cache/direct:    list[i] = { data: { metadata, spec, status } }(嵌套)
	// 各 binding 用 json.RawMessage 二阶段解 —— 先取 data.list,再尝试 .data 包一层 / 不包一层。
	type k8sDep struct {
		Metadata struct {
			Name, Namespace string
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
			Strategy struct {
				Type string `json:"type"`
			} `json:"strategy"`
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
		} `json:"spec"`
		Status struct {
			UpdatedReplicas   int `json:"updatedReplicas"`
			ReadyReplicas     int `json:"readyReplicas"`
			AvailableReplicas int `json:"availableReplicas"`
			Conditions        []struct {
				Type, Status, Reason string
			} `json:"conditions"`
		} `json:"status"`
	}
	// 兼容 Kuboard v4 多种分页字段命名:list / items / records / content / rows
	var outer struct {
		Data struct {
			List    []json.RawMessage `json:"list"`
			Items   []json.RawMessage `json:"items"`
			Records []json.RawMessage `json:"records"`
			Content []json.RawMessage `json:"content"`
			Rows    []json.RawMessage `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("解析 deployments 失败:%v;URL=%s;原始=%s", err, hitURL, snippet(raw))
	}
	listItems := outer.Data.List
	if len(listItems) == 0 {
		listItems = outer.Data.Items
	}
	if len(listItems) == 0 {
		listItems = outer.Data.Records
	}
	if len(listItems) == 0 {
		listItems = outer.Data.Content
	}
	if len(listItems) == 0 {
		listItems = outer.Data.Rows
	}
	if len(listItems) == 0 {
		// 五种命名都空 —— 把 Kuboard 实际响应的前 600 字节灌进 error,让用户能看到字段名
		return nil, fmt.Errorf("Kuboard 返回里没识别出 list 字段(试过 list/items/records/content/rows);URL=%s;响应前 600 字节=%s",
			hitURL, snippetN(raw, 600))
	}
	out := make([]KuboardDeploymentInfo, 0, len(listItems))
	for _, item := range listItems {
		var dep k8sDep
		// 先按平铺解;若 metadata.name 是空,fallback 到嵌套 .data 形态
		if err := json.Unmarshal(item, &dep); err != nil || dep.Metadata.Name == "" {
			var wrapped struct {
				Data k8sDep `json:"data"`
			}
			if err2 := json.Unmarshal(item, &wrapped); err2 == nil && wrapped.Data.Metadata.Name != "" {
				dep = wrapped.Data
			} else {
				continue
			}
		}
		info := KuboardDeploymentInfo{
			Name: dep.Metadata.Name, Namespace: dep.Metadata.Namespace,
			Replicas: dep.Spec.Replicas, Strategy: dep.Spec.Strategy.Type,
			UpdatedReplicas:   dep.Status.UpdatedReplicas,
			ReadyReplicas:     dep.Status.ReadyReplicas,
			AvailableReplicas: dep.Status.AvailableReplicas,
		}
		for _, c := range dep.Status.Conditions {
			tag := c.Type + "=" + c.Status
			if c.Reason != "" {
				tag += " (" + c.Reason + ")"
			}
			info.Conditions = append(info.Conditions, tag)
		}
		if len(dep.Spec.Selector.MatchLabels) > 0 {
			parts := make([]string, 0, len(dep.Spec.Selector.MatchLabels))
			keys := make([]string, 0, len(dep.Spec.Selector.MatchLabels))
			for k := range dep.Spec.Selector.MatchLabels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, k+"="+dep.Spec.Selector.MatchLabels[k])
			}
			info.Selector = strings.Join(parts, ",")
		}
		out = append(out, info)
	}
	return out, nil
}

// ── 一站式快照:KuboardPodSnapshot ─────────────────────────────────────
// 排障最常用的入口 binding:一次拿"pod 列表 + 最近 events + 主 pod 的当前/历史 logs"。
// agent 拿到这份就能直接判断 pod 是否健康 + 给出根因方向。

type KuboardPodSnapshotInput struct {
	URL           string `json:"url"`
	AccessKey     string `json:"access_key,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"label_selector,omitempty"`
	PodNameFilter string `json:"pod_name_filter,omitempty"`
	TailLines     int    `json:"tail_lines,omitempty"` // 默认 200
}

type KuboardPodSnapshotEntry struct {
	Pod          KuboardPodInfo `json:"pod"`
	Events       []KuboardEvent `json:"events,omitempty"`        // 该 pod 相关事件
	LogsCurrent  string         `json:"logs_current,omitempty"`  // 主容器当前日志 tail
	LogsPrevious string         `json:"logs_previous,omitempty"` // 主容器上次日志(restartCount>0 才查)
}

type KuboardPodSnapshotResult struct {
	Pods  []KuboardPodSnapshotEntry `json:"pods"`
	Notes []string                  `json:"notes,omitempty"` // 部分 pod 取日志失败的原因
}

func (a *App) KuboardPodSnapshot(in KuboardPodSnapshotInput) (*KuboardPodSnapshotResult, error) {
	pods, err := a.KuboardListPods(KuboardListPodsInput{
		URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
		Cluster: in.Cluster, Namespace: in.Namespace,
		LabelSelector: in.LabelSelector, PodNameFilter: in.PodNameFilter,
	})
	if err != nil {
		return nil, err
	}
	res := &KuboardPodSnapshotResult{}
	for _, p := range pods {
		entry := KuboardPodSnapshotEntry{Pod: p}
		// events: 用 fieldSelector 精确到这个 pod
		evts, err := a.KuboardListEvents(KuboardListEventsInput{
			URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
			Cluster: in.Cluster, Namespace: in.Namespace,
			FieldSelector: "involvedObject.name=" + p.Name,
			OnlyWarnings:  false,
			Limit:         20,
		})
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("pod %s 取 events 失败: %v", p.Name, err))
		} else {
			entry.Events = evts
		}
		// 当前日志 tail
		mainContainer := ""
		if len(p.Containers) > 0 {
			mainContainer = p.Containers[0].Name
		}
		curLogs, err := a.KuboardGetPodLogs(KuboardGetPodLogsInput{
			URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
			Cluster: in.Cluster, Namespace: in.Namespace,
			PodName: p.Name, Container: mainContainer, TailLines: in.TailLines, Previous: false,
		})
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("pod %s 取当前日志失败: %v", p.Name, err))
		} else {
			entry.LogsCurrent = curLogs
		}
		// 历史日志:仅 restartCount>0 才拉(不然 K8s 会返 400 "previous terminated container not found")
		if p.RestartCount > 0 {
			prevLogs, err := a.KuboardGetPodLogs(KuboardGetPodLogsInput{
				URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
				Cluster: in.Cluster, Namespace: in.Namespace,
				PodName: p.Name, Container: mainContainer, TailLines: in.TailLines, Previous: true,
			})
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("pod %s 取上次日志失败: %v", p.Name, err))
			} else {
				entry.LogsPrevious = prevLogs
			}
		}
		res.Pods = append(res.Pods, entry)
	}
	return res, nil
}
