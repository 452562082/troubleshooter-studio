package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"os/signal"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/initwizard"
)

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	output := fs.String("o", "system.yaml", "输出 system.yaml 路径")
	input := fs.String("i", "", "可选：已有 system.yaml，用作字段预填（改动哪里就回答哪里，其余回车接受）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 覆盖保护
	if _, err := os.Stat(*output); err == nil {
		fmt.Printf("%s 已存在。覆盖吗？[y/N]: ", *output)
		var ans string
		fmt.Scanln(&ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("aborted by user")
		}
	}

	w := initwizard.New(os.Stdin, os.Stdout)

	// 预填来源优先级：-i > ~/.tshoot/init-draft.yaml > 无
	draftPath := filepath.Join(initDraftDir(), "init-draft.yaml")
	if *input != "" {
		cfg, err := config.Load(*input)
		if err != nil {
			return fmt.Errorf("load -i %s: %w", *input, err)
		}
		w.Defaults = answersFromConfig(cfg)
		fmt.Printf("[prefill] 从 %s 预填字段；回车接受已有值\n\n", *input)
	} else if info, err := os.Stat(draftPath); err == nil {
		fmt.Printf("[draft] 检测到上次中断的草稿 %s（%s 前）\n", draftPath, humanSince(info.ModTime()))
		fmt.Print("  继续用它作为预填？[Y/n]: ")
		var ans string
		fmt.Scanln(&ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans == "" || ans == "y" || ans == "yes" {
			if cfg, err := config.Load(draftPath); err == nil {
				w.Defaults = answersFromConfig(cfg)
				fmt.Println()
			} else {
				fmt.Fprintf(os.Stderr, "  草稿解析失败（忽略）：%v\n\n", err)
			}
		} else {
			_ = os.Remove(draftPath)
			fmt.Println("  已丢弃草稿")
			fmt.Println()
		}
	}

	// signal handler：Ctrl+C 把当前快照落盘为 draft，下次可续
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		if snap := w.Snapshot(); snap != nil {
			_ = os.MkdirAll(initDraftDir(), 0o755)
			if f, err := os.Create(draftPath); err == nil {
				_ = snap.WriteYAML(f)
				_ = f.Close()
				fmt.Fprintf(os.Stderr, "\n[中断] 已保存草稿 → %s\n  下次 tshoot init 会询问是否继续\n", draftPath)
			}
		} else {
			fmt.Fprintln(os.Stderr, "\n[中断]")
		}
		os.Exit(130)
	}()

	ans, err := w.Run()
	if err != nil {
		return err
	}
	// 正常完成 → 删除草稿
	_ = os.Remove(draftPath)

	f, err := os.Create(*output)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ans.WriteYAML(f); err != nil {
		return err
	}
	fmt.Printf("\n[ok] wrote %s\n", *output)
	fmt.Println("下一步:")
	fmt.Printf("  tshoot validate -i %s\n", *output)
	fmt.Printf("  tshoot gen -i %s\n", *output)
	return nil
}

func initDraftDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".tshoot")
	}
	return filepath.Join(os.TempDir(), "tshoot")
}

func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}

// answersFromConfig 把已有 SystemConfig 转成 init 向导的 Answers，供 -i 预填或续 draft 使用。
// 未显式声明的字段走 wizard 的原生默认。
func answersFromConfig(cfg *config.SystemConfig) *initwizard.Answers {
	a := &initwizard.Answers{
		SystemID:             cfg.System.ID,
		SystemName:           cfg.System.Name,
		SystemDescription:    cfg.System.Description,
		AgentName:            cfg.Agent.Name,
		AgentModel:           cfg.Agent.Model,
		WorkspaceName:        cfg.Agent.WorkspaceName,
		ConfigCenterType:     cfg.Infrastructure.PrimaryConfigCenter().Type,
		GrafanaEnabled:       cfg.Infrastructure.Observability.Grafana.Enabled,
		LokiEnabled:          cfg.Infrastructure.Observability.Loki.Enabled,
		PrometheusEnabled:    cfg.Infrastructure.Observability.Prometheus.Enabled,
		DataStoresEnabled:    map[string]bool{},
		Targets:              cfg.Generation.Targets,
		FeishuProjectEnabled: false,
	}
	for _, e := range cfg.Environments {
		a.Envs = append(a.Envs, initwizard.EnvAnswer{ID: e.ID, APIDomain: e.APIDomain, IsProd: e.IsProd})
	}
	for _, r := range cfg.Repos {
		branches := map[string]string{}
		for k, v := range r.EnvBranches {
			branches[k] = v
		}
		a.Repos = append(a.Repos, initwizard.RepoAnswer{
			Name: r.Name, URL: r.URL, Stack: r.Stack, Framework: r.Framework,
			ServiceNames: r.ServiceNames, EnvBranches: branches,
		})
	}
	for _, ds := range cfg.Infrastructure.DataStores {
		a.DataStoresEnabled[ds.Type] = ds.Enabled
	}
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Platform == "lark" && m.Enabled {
			a.LarkEnabled = true
			a.LarkAttachment = m.AttachmentSend
		}
	}
	for _, pt := range cfg.Infrastructure.ProjectTracking {
		if pt.Platform == "feishu_project" && pt.Enabled {
			a.FeishuProjectEnabled = true
		}
	}
	return a
}
