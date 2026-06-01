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

// parseK8sDeployment 把一个标准 k8s Deployment 对象(v3 的 items[i] / v4 的 list[i].data
// 都喂它)解析成 KuboardDeploymentInfo。解不出 name 返回 ok=false,调用方跳过。
func parseK8sDeployment(raw json.RawMessage) (KuboardDeploymentInfo, bool) {
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
	var dep k8sDep
	if err := json.Unmarshal(raw, &dep); err != nil || dep.Metadata.Name == "" {
		return KuboardDeploymentInfo{}, false
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
	return info, true
}

func (a *App) KuboardListDeployments(in KuboardListPodsInput) ([]KuboardDeploymentInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()
	// deployment 在 apps/v1(非 core),v3/v4 走 listK8sObjectsGroup 统一收敛:
	//   v3: GET {base}/k8s-api/{cluster}/apis/apps/v1/namespaces/{ns}/deployments  → {items:[<Deployment>]}
	//   v4: GET {base}/api/cluster.kuboard.cn/v4/cluster-cache?...apiGroup=apps&resource=deployments
	//       &clusterIdNamespaces={uid}/{ns}...                                     → {data:{list:[{data:<Deployment>}]}}
	objs, err := s.listK8sObjectsGroup("apis/apps/v1", "apps", "deployments", in.Namespace, "")
	if err != nil {
		return nil, err
	}
	out := make([]KuboardDeploymentInfo, 0, len(objs))
	for _, raw := range objs {
		info, ok := parseK8sDeployment(raw)
		if !ok {
			continue
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
