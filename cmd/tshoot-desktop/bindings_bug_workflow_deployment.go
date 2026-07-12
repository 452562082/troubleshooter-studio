package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type caseConfiguredDeploymentVerifier struct {
	app   *App
	store *bughub.CaseStore
}

func (a *App) configuredDeploymentProvider(ctx context.Context, caseID string) string {
	if a == nil || a.workflowStore == nil {
		return "deployment-config"
	}
	incident, err := a.workflowStore.GetCase(ctx, strings.TrimSpace(caseID))
	if err != nil {
		return "deployment-config"
	}
	loader := a.workflowLoadDeploymentConfig
	if loader == nil {
		loader = a.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil || cfg.System.ID != incident.SystemID {
		return "deployment-config"
	}
	for _, environment := range cfg.Environments {
		if environment.ID == incident.Environment {
			return environment.DeploymentVerification.EffectiveProvider()
		}
	}
	return "deployment-config"
}

func (v *caseConfiguredDeploymentVerifier) unavailable(request bughub.DeploymentVerificationRequest, code, message string) bughub.DeploymentObservation {
	return bughub.DeploymentObservation{Environment: request.Environment, ExpectedCommits: bughub.CloneStringMap(request.ExpectedCommits), VerificationSource: "deployment-config", ObservedAt: time.Now().UTC(), DiagnosticCode: code, DiagnosticMessage: message, Result: bughub.DeploymentResultUnavailable}
}

func (v *caseConfiguredDeploymentVerifier) Verify(ctx context.Context, request bughub.DeploymentVerificationRequest) (bughub.DeploymentObservation, error) {
	if v == nil || v.app == nil || v.store == nil {
		return v.unavailable(request, "config_unavailable", "部署版本验证配置不可用"), bughub.ErrDeploymentVerifierUnavailable
	}
	incident, err := v.store.GetCase(ctx, request.CaseID)
	if err != nil || incident.Environment != request.Environment {
		return v.unavailable(request, "case_scope_mismatch", "Case 环境与验证请求不一致"), bughub.ErrDeploymentVerifierUnavailable
	}
	loader := v.app.workflowLoadDeploymentConfig
	if loader == nil {
		loader = v.app.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil || cfg.System.ID != incident.SystemID {
		return v.unavailable(request, "config_unavailable", "无法读取 Case 对应的机器人配置"), bughub.ErrDeploymentVerifierUnavailable
	}
	var environment *config.Environment
	for i := range cfg.Environments {
		if cfg.Environments[i].ID == incident.Environment {
			environment = &cfg.Environments[i]
			break
		}
	}
	if environment == nil {
		return v.unavailable(request, "environment_unknown", "机器人配置中不存在 Case 环境"), bughub.ErrDeploymentVerifierUnavailable
	}
	provider := environment.DeploymentVerification.EffectiveProvider()
	// Source was resolved by the server before reservation. Re-reading the
	// config here closes the TOCTOU window: a provider change between reserve
	// and execute fails closed instead of running a different verifier.
	if strings.ToLower(strings.TrimSpace(request.Source)) != provider {
		return v.unavailable(request, "config_changed", "部署版本验证配置已变化，请重新确认部署"), bughub.ErrDeploymentVerifierUnavailable
	}
	request.Source = provider
	switch provider {
	case config.DeploymentVerificationProviderManual:
		return (bughub.ManualVersionVerifier{Environment: incident.Environment}).Verify(ctx, request)
	case config.DeploymentVerificationProviderHTTP:
		return (bughub.HTTPVersionVerifier{Environment: incident.Environment, Config: environment.DeploymentVerification.HTTP}).Verify(ctx, request)
	case config.DeploymentVerificationProviderK8s:
		factory := v.app.workflowK8sReaderFactory
		if factory == nil {
			factory = v.app.newKuboardDeploymentReader
		}
		reader, readerErr := factory(ctx, cfg, *environment)
		if readerErr != nil || reader == nil {
			return v.unavailable(request, "k8s_reader_unavailable", "K8s 只读运行时不可用"), bughub.ErrDeploymentVerifierUnavailable
		}
		return (bughub.K8sVersionVerifier{Environment: incident.Environment, Config: environment.DeploymentVerification.K8s, Reader: reader}).Verify(ctx, request)
	default:
		return v.unavailable(request, "provider_unknown", "部署版本验证方式不受支持"), bughub.ErrDeploymentVerifierUnavailable
	}
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
	return nil, errors.New("Kuboard endpoint unavailable")
}

func (r *kuboardDeploymentVersionReader) ReadDeployment(ctx context.Context, cluster, namespace, deployment string) (bughub.K8sDeploymentVersion, error) {
	s, err := kuboardSetup(ctx, r.endpoint.URL, r.endpoint.AccessKey, r.endpoint.Username, r.endpoint.Password, cluster)
	if err != nil {
		return bughub.K8sDeploymentVersion{}, errors.New("Kuboard setup failed")
	}
	defer s.cancel()
	objects, err := s.listK8sObjectsGroup("apis/apps/v1", "apps", "deployments", namespace, "")
	if err != nil {
		return bughub.K8sDeploymentVersion{}, errors.New("Deployment read failed")
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
	return bughub.K8sDeploymentVersion{}, errors.New("Deployment not found")
}

var _ bughub.DeploymentVerifier = (*caseConfiguredDeploymentVerifier)(nil)
var _ bughub.K8sDeploymentReader = (*kuboardDeploymentVersionReader)(nil)
