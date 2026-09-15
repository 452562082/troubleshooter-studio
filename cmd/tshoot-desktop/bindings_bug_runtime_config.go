package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

func normalizedVerifierURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "invalid"
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func k8sVerifierEndpointIdentity(cfg *config.SystemConfig, environmentID string) (string, bool) {
	for _, endpoint := range cfg.Infrastructure.Observability.K8sRuntime.Endpoints {
		if endpoint.Env != environmentID {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(endpoint.URL))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "invalid", false
		}
		return normalizedVerifierURL(endpoint.URL), true
	}
	return "unconfigured", false
}
func (a *App) loadInstalledIncidentConfig(_ context.Context, incident bughub.IncidentCase) (*config.SystemConfig, error) {
	loadBot := a.workflowLoadBot
	var bot bughub.BotRef
	var err error
	if loadBot != nil {
		bot, err = loadBot(incident.SelectedBotKey)
	} else {
		bots, listErr := a.bugBotRefs()
		if listErr != nil {
			return nil, listErr
		}
		for _, candidate := range bots {
			if candidate.Key == incident.SelectedBotKey {
				bot = candidate
				break
			}
		}
		if bot.Key == "" {
			err = os.ErrNotExist
		}
	}
	if err != nil || bot.SystemID != incident.SystemID {
		return nil, errors.New("incident bot configuration unavailable")
	}
	root := strings.TrimSpace(bot.Path)
	if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
		root = filepath.Dir(root)
	}
	data, err := os.ReadFile(filepath.Join(root, discover.MetaFilename))
	if err != nil {
		return nil, errors.New("incident bot metadata unavailable")
	}
	var meta discover.Meta
	if json.Unmarshal(data, &meta) != nil || meta.SystemID != incident.SystemID || strings.TrimSpace(meta.TroubleshooterYAML) == "" {
		return nil, errors.New("incident bot metadata invalid")
	}
	cfg, err := config.LoadFromBytes([]byte(meta.TroubleshooterYAML))
	if err != nil {
		return nil, errors.New("incident bot configuration invalid")
	}
	return cfg, nil
}

type kuboardDeploymentVersionReader struct{ endpoint config.ObsEndpoint }

func (a *App) newKuboardDeploymentReader(_ context.Context, cfg *config.SystemConfig, environment config.Environment) (bughub.K8sDeploymentReader, error) {
	runtimeCfg := cfg.Infrastructure.Observability.K8sRuntime
	if provider := strings.ToLower(strings.TrimSpace(runtimeCfg.Provider)); provider != "" && provider != "kuboard" {
		return nil, errors.New("configured K8s runtime is not Kuboard")
	}
	for _, endpoint := range runtimeCfg.Endpoints {
		if endpoint.Env == environment.ID && strings.TrimSpace(endpoint.URL) != "" {
			return &kuboardDeploymentVersionReader{endpoint: endpoint}, nil
		}
	}
	return nil, errors.New("kuboard endpoint unavailable")
}
func (r *kuboardDeploymentVersionReader) ReadDeployment(ctx context.Context, cluster, namespace, deployment string) (bughub.K8sDeploymentVersion, error) {
	s, err := kuboardSetup(ctx, r.endpoint.URL, r.endpoint.AccessKey, r.endpoint.Username, r.endpoint.Password, cluster)
	if err != nil {
		return bughub.K8sDeploymentVersion{}, errors.New("kuboard setup failed")
	}
	defer s.cancel()
	objects, err := s.listK8sObjectsGroup("apis/apps/v1", "apps", "deployments", namespace, "")
	if err != nil {
		return bughub.K8sDeploymentVersion{}, errors.New("deployment read failed")
	}
	for _, raw := range objects {
		var value struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Metadata struct {
						Labels map[string]string `json:"labels"`
					} `json:"metadata"`
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Metadata.Name != deployment {
			continue
		}
		out := bughub.K8sDeploymentVersion{Annotations: value.Metadata.Annotations, Labels: value.Spec.Template.Metadata.Labels}
		for _, container := range value.Spec.Template.Spec.Containers {
			if container.Image != "" {
				out.Images = append(out.Images, container.Image)
			}
		}
		return out, nil
	}
	return bughub.K8sDeploymentVersion{}, errors.New("deployment not found")
}
