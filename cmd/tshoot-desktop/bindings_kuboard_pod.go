// bindings_kuboard_pod.go —— Kuboard 运行时 Pod / 日志 / Events 查询 binding。
//
// 让机器人能直接通过 Kuboard 查 pod / events / logs,排障时不必让用户手动开 kubectl。
// 底层走 /api/cluster.kuboard.cn/v4/cluster-cache/direct (resource=pods/events/...),
// pod logs 走 /api/cluster.kuboard.cn/v4/pod-logs。鉴权 + 集群 ID 解析复用 kuboardSetup。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// KuboardPodInfo 单个 pod 的精简快照,UI / agent 可直接消费,不用再解 K8s 完整 pod schema。
type KuboardPodInfo struct {
	Name         string                 `json:"name"`
	Namespace    string                 `json:"namespace"`
	Status       string                 `json:"status"`    // Running / Pending / CrashLoopBackOff / Succeeded / Failed / Unknown
	Phase        string                 `json:"phase"`     // 原始 spec 的 phase
	NodeName     string                 `json:"node_name"` // 调度到哪个 node
	PodIP        string                 `json:"pod_ip"`
	StartTime    string                 `json:"start_time"`    // RFC3339
	RestartCount int                    `json:"restart_count"` // 主容器累计 restart
	Containers   []KuboardContainerStat `json:"containers"`
	Reason       string                 `json:"reason,omitempty"` // OOMKilled / Error / Completed 等
	Message      string                 `json:"message,omitempty"`
}

// KuboardContainerStat 容器级状态(不含日志,日志另调 KuboardGetPodLogs)
type KuboardContainerStat struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restart_count"`
	State        string `json:"state"`                 // running / waiting / terminated
	WaitReason   string `json:"wait_reason,omitempty"` // ImagePullBackOff / CrashLoopBackOff / ContainerCreating
	TermReason   string `json:"term_reason,omitempty"` // OOMKilled / Error / Completed
	TermExitCode int    `json:"term_exit_code,omitempty"`
}

// KuboardListPodsInput 查 pod 列表的入参。labelSelector 可选(如 "app=order-service");
// 全留空就拉这个 ns 的全部 pod。
type KuboardListPodsInput struct {
	URL           string `json:"url"`
	AccessKey     string `json:"access_key,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"label_selector,omitempty"`
	PodNameFilter string `json:"pod_name_filter,omitempty"` // 子串匹配,空 = 不过滤
}

func (a *App) KuboardListPods(in KuboardListPodsInput) ([]KuboardPodInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()

	q := fmt.Sprintf("resource=pods&namespace=%s", url.QueryEscape(in.Namespace))
	if in.LabelSelector != "" {
		q += "&labelSelector=" + url.QueryEscape(in.LabelSelector)
	}
	raw, err := kuboardDirectGET(s, q)
	if err != nil {
		return nil, err
	}
	// pods list 形:{data:{list:[{data:{metadata,spec,status}}]}}
	var v struct {
		Data struct {
			List []struct {
				Data k8sPod `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 pod 列表失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]KuboardPodInfo, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		p := summarizePod(it.Data)
		if in.PodNameFilter != "" && !strings.Contains(p.Name, in.PodNameFilter) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// k8sPod 对应 K8s Pod 资源的最小子集,只取 summarize 用得到的字段
type k8sPod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		Reason            string `json:"reason"`
		Message           string `json:"message"`
		PodIP             string `json:"podIP"`
		StartTime         string `json:"startTime"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Image        string `json:"image"`
			Ready        bool   `json:"ready"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting    *struct{ Reason, Message string } `json:"waiting,omitempty"`
				Running    *struct{ StartedAt string }       `json:"running,omitempty"`
				Terminated *struct {
					Reason   string `json:"reason"`
					ExitCode int    `json:"exitCode"`
				} `json:"terminated,omitempty"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// summarizePod 把 k8s pod 缩成排障最常看的字段。
// status 字段优先从 containerStatuses[].state.waiting.reason 取(那才是 CrashLoopBackOff
// 这种"机器人最关心"的状态),为空再用 status.phase。
func summarizePod(p k8sPod) KuboardPodInfo {
	out := KuboardPodInfo{
		Name:      p.Metadata.Name,
		Namespace: p.Metadata.Namespace,
		Phase:     p.Status.Phase,
		NodeName:  p.Spec.NodeName,
		PodIP:     p.Status.PodIP,
		StartTime: p.Status.StartTime,
		Reason:    p.Status.Reason,
		Message:   p.Status.Message,
	}
	displayStatus := p.Status.Phase
	totalRestart := 0
	for _, c := range p.Status.ContainerStatuses {
		stat := KuboardContainerStat{
			Name: c.Name, Image: c.Image, Ready: c.Ready, RestartCount: c.RestartCount,
		}
		switch {
		case c.State.Waiting != nil:
			stat.State = "waiting"
			stat.WaitReason = c.State.Waiting.Reason
			// CrashLoopBackOff / ImagePullBackOff 这类比 phase=Pending 更具体,提前到 displayStatus
			if displayStatus == "Pending" || displayStatus == "Running" {
				if c.State.Waiting.Reason != "" {
					displayStatus = c.State.Waiting.Reason
				}
			}
		case c.State.Terminated != nil:
			stat.State = "terminated"
			stat.TermReason = c.State.Terminated.Reason
			stat.TermExitCode = c.State.Terminated.ExitCode
		case c.State.Running != nil:
			stat.State = "running"
		}
		totalRestart += c.RestartCount
		out.Containers = append(out.Containers, stat)
	}
	out.Status = displayStatus
	out.RestartCount = totalRestart
	return out
}

// KuboardGetPodLogsInput 查 pod logs 入参。previous=true 拉上一次容器的日志(CrashLoopBackOff 排障关键)
type KuboardGetPodLogsInput struct {
	URL       string `json:"url"`
	AccessKey string `json:"access_key,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	PodName   string `json:"pod_name"`
	Container string `json:"container,omitempty"`  // 多容器 pod 必填,单容器可省
	TailLines int    `json:"tail_lines,omitempty"` // 默认 200
	Previous  bool   `json:"previous,omitempty"`
}

// KuboardGetPodLogs 拉容器日志。Kuboard v4 暴露 /pod-logs 接口(直接代理到 K8s API)。
func (a *App) KuboardGetPodLogs(in KuboardGetPodLogsInput) (string, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return "", err
	}
	defer s.cancel()

	tail := in.TailLines
	if tail <= 0 {
		tail = 200
	}
	// Kuboard v4 logs:走 cluster-cache/direct 的子路径或专用 /pod-logs;
	// 兼容性:实测 cluster-cache/direct 不返 logs,需 /pod-logs 端点
	q := fmt.Sprintf("clusterId=%s&namespace=%s&podName=%s&tailLines=%d&previous=%v",
		s.clusterUID, url.QueryEscape(in.Namespace), url.QueryEscape(in.PodName), tail, in.Previous)
	if in.Container != "" {
		q += "&container=" + url.QueryEscape(in.Container)
	}
	u := s.base + "/api/cluster.kuboard.cn/v4/pod-logs?" + q
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Kb-Access-Key", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d:%s", resp.StatusCode, snippet(raw))
	}
	// Kuboard 可能用 {data:"<logs>"} 包一层,也可能直返 plain text
	var v struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err == nil && v.Data != "" {
		return v.Data, nil
	}
	return string(raw), nil
}

// ── Events 查询 ──────────────────────────────────────────────────────

// KuboardEvent 事件简化表示
type KuboardEvent struct {
	Type           string `json:"type"`   // Normal / Warning
	Reason         string `json:"reason"` // FailedScheduling / OOMKilled / BackOff ...
	Message        string `json:"message"`
	InvolvedObject string `json:"involved_object"` // <Kind>/<name>
	Count          int    `json:"count"`
	FirstTimestamp string `json:"first_timestamp"`
	LastTimestamp  string `json:"last_timestamp"`
}

// KuboardListEventsInput 查 events 入参
type KuboardListEventsInput struct {
	URL           string `json:"url"`
	AccessKey     string `json:"access_key,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace"`
	FieldSelector string `json:"field_selector,omitempty"` // 例:"involvedObject.name=order-pod-xxx"
	OnlyWarnings  bool   `json:"only_warnings,omitempty"`
	Limit         int    `json:"limit,omitempty"` // 默认 20
}

func (a *App) KuboardListEvents(in KuboardListEventsInput) ([]KuboardEvent, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	q := fmt.Sprintf("resource=events&namespace=%s", url.QueryEscape(in.Namespace))
	if in.FieldSelector != "" {
		q += "&fieldSelector=" + url.QueryEscape(in.FieldSelector)
	}
	raw, err := kuboardDirectGET(s, q)
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			List []struct {
				Data struct {
					Type           string `json:"type"`
					Reason         string `json:"reason"`
					Message        string `json:"message"`
					Count          int    `json:"count"`
					InvolvedObject struct {
						Kind, Name string
					} `json:"involvedObject"`
					FirstTimestamp string `json:"firstTimestamp"`
					LastTimestamp  string `json:"lastTimestamp"`
				} `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 events 失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]KuboardEvent, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		if in.OnlyWarnings && it.Data.Type != "Warning" {
			continue
		}
		out = append(out, KuboardEvent{
			Type: it.Data.Type, Reason: it.Data.Reason, Message: it.Data.Message,
			InvolvedObject: it.Data.InvolvedObject.Kind + "/" + it.Data.InvolvedObject.Name,
			Count:          it.Data.Count,
			FirstTimestamp: it.Data.FirstTimestamp, LastTimestamp: it.Data.LastTimestamp,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
