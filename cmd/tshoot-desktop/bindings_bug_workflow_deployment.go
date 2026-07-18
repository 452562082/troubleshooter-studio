package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type deploymentVerifierBinding struct {
	Provider, Fingerprint string
	Snapshot              json.RawMessage
}

type deploymentRepositoryMapping struct {
	Repo       string `json:"repo"`
	Deployment string `json:"deployment"`
}

type caseConfiguredDeploymentVerifier struct {
	app   *App
	store *bughub.CaseStore
}

func (a *App) configuredDeploymentBinding(ctx context.Context, caseID string) deploymentVerifierBinding {
	if a == nil || a.workflowStore == nil {
		return unavailableDeploymentBinding()
	}
	incident, err := a.workflowStore.GetCase(ctx, strings.TrimSpace(caseID))
	if err != nil {
		return unavailableDeploymentBinding()
	}
	loader := a.workflowLoadDeploymentConfig
	if loader == nil {
		loader = a.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil || cfg.System.ID != incident.SystemID {
		return unavailableDeploymentBinding()
	}
	for _, environment := range cfg.Environments {
		if environment.ID == incident.Environment {
			return canonicalDeploymentVerifierBinding(cfg, environment)
		}
	}
	return unavailableDeploymentBinding()
}

func unavailableDeploymentBinding() deploymentVerifierBinding {
	return makeDeploymentBinding("deployment-config", map[string]any{"provider": "unavailable", "policy": "deployment-config-v1"})
}

func (a *App) deploymentVerificationPreview(ctx context.Context, caseID string) IncidentDeploymentVerification {
	binding := a.configuredDeploymentBinding(ctx, caseID)
	preview := IncidentDeploymentVerification{Provider: binding.Provider, Available: binding.Provider != "deployment-config"}
	var snapshot map[string]any
	_ = json.Unmarshal(binding.Snapshot, &snapshot)
	switch binding.Provider {
	case "manual":
		preview.Hint = "仅确认已部署；不要求填写版本"
	case "http":
		preview.Hint = "HTTP 版本接口自动采集 · " + fmt.Sprint(snapshot["json_pointer"])
	case "k8s":
		preview.Hint = "复用可观测性 K8s 映射自动采集；采集不到不阻塞回归"
	default:
		preview.Provider = "unavailable"
		preview.Hint = "无法读取当前 Case 的部署验证配置"
	}
	return preview
}

func makeDeploymentBinding(provider string, snapshot any) deploymentVerifierBinding {
	raw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(raw)
	return deploymentVerifierBinding{Provider: provider, Fingerprint: fmt.Sprintf("%x", sum[:]), Snapshot: raw}
}

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

func canonicalDeploymentVerifierBinding(cfg *config.SystemConfig, environment config.Environment) deploymentVerifierBinding {
	verification, err := cfg.DeploymentVerificationForEnvironment(environment.ID)
	if err != nil {
		return unavailableDeploymentBinding()
	}
	provider := verification.EffectiveProvider()
	switch provider {
	case config.DeploymentVerificationProviderManual:
		return makeDeploymentBinding(provider, map[string]any{"provider": provider, "policy": "manual-confirmation-v2-no-version-input"})
	case config.DeploymentVerificationProviderHTTP:
		return makeDeploymentBinding(provider, map[string]any{"provider": provider, "url": normalizedVerifierURL(verification.HTTP.URL), "json_pointer": verification.HTTP.JSONPointer, "allow_private": verification.HTTP.AllowPrivate, "policy": "http-v3-optional-version-ssrf-dialguard-timeout10s-body1m-samehost"})
	case config.DeploymentVerificationProviderK8s:
		endpointIdentity, _ := k8sVerifierEndpointIdentity(cfg, environment.ID)
		endpointProvider := strings.ToLower(strings.TrimSpace(cfg.Infrastructure.Observability.K8sRuntime.Provider))
		if endpointProvider == "" {
			endpointProvider = "kuboard"
		}
		k := verification.K8s
		repositories := make([]string, 0, len(k.DeploymentsByRepo))
		for repo := range k.DeploymentsByRepo {
			repositories = append(repositories, repo)
		}
		sort.Strings(repositories)
		mappings := make([]deploymentRepositoryMapping, 0, len(repositories))
		for _, repo := range repositories {
			mappings = append(mappings, deploymentRepositoryMapping{Repo: repo, Deployment: k.DeploymentsByRepo[repo]})
		}
		return makeDeploymentBinding(provider, map[string]any{"provider": provider, "cluster": k.Cluster, "namespace": k.Namespace, "deployments_by_repo": mappings, "commit_annotation": k.CommitAnnotation, "image_label": k.ImageLabel, "endpoint_provider": endpointProvider, "endpoint_identity": endpointIdentity, "policy": "k8s-readonly-v2-observability-map-optional-version"})
	default:
		return unavailableDeploymentBinding()
	}
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
	verification, err := cfg.DeploymentVerificationForEnvironment(environment.ID)
	if err != nil {
		return v.unavailable(request, "environment_unknown", "机器人配置中不存在 Case 环境"), bughub.ErrDeploymentVerifierUnavailable
	}
	provider := verification.EffectiveProvider()
	binding := canonicalDeploymentVerifierBinding(cfg, *environment)
	// Source was resolved by the server before reservation. Re-reading the
	// config here closes the TOCTOU window: a provider change between reserve
	// and execute fails closed instead of running a different verifier.
	if strings.ToLower(strings.TrimSpace(request.Source)) != provider || request.ConfigFingerprint != binding.Fingerprint || string(request.ConfigSnapshot) != string(binding.Snapshot) {
		return v.unavailable(request, "config_changed", "部署版本验证配置已变化，请重新确认部署"), bughub.ErrDeploymentVerifierUnavailable
	}
	request.Source = provider
	switch provider {
	case config.DeploymentVerificationProviderManual:
		return bughub.DeploymentObservation{
			Environment: incident.Environment, ExpectedCommits: bughub.CloneStringMap(request.ExpectedCommits),
			VerificationSource: "manual", ObservedAt: time.Now().UTC(), Result: bughub.DeploymentResultUnavailable,
			DiagnosticCode: "version_not_collected", DiagnosticMessage: "已确认部署；未要求用户填写版本",
		}, nil
	case config.DeploymentVerificationProviderHTTP:
		return (bughub.HTTPVersionVerifier{Environment: incident.Environment, Config: verification.HTTP}).Verify(ctx, request)
	case config.DeploymentVerificationProviderK8s:
		if _, safe := k8sVerifierEndpointIdentity(cfg, environment.ID); !safe {
			return v.unavailable(request, "k8s_endpoint_invalid", "K8s 只读运行时端点未配置或包含不安全 URL 字段"), bughub.ErrDeploymentVerifierUnavailable
		}
		factory := v.app.workflowK8sReaderFactory
		if factory == nil {
			factory = v.app.newKuboardDeploymentReader
		}
		reader, readerErr := factory(ctx, cfg, *environment)
		if readerErr != nil || reader == nil {
			return v.unavailable(request, "k8s_reader_unavailable", "K8s 只读运行时不可用"), bughub.ErrDeploymentVerifierUnavailable
		}
		return (bughub.K8sVersionVerifier{Environment: incident.Environment, Config: verification.K8s, Reader: reader}).Verify(ctx, request)
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
