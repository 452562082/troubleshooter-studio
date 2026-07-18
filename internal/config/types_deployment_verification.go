package config

import (
	"fmt"
	"strings"
)

const (
	DeploymentVerificationProviderManual = "manual"
	DeploymentVerificationProviderHTTP   = "http"
	DeploymentVerificationProviderK8s    = "k8s"
)

// DeploymentVerificationConfig selects the read-only runtime proof used after
// a human deployment. An empty provider deliberately means manual for legacy
// YAML compatibility.
type DeploymentVerificationConfig struct {
	Provider string                     `yaml:"provider,omitempty"`
	HTTP     HTTPDeploymentVerification `yaml:"http,omitempty"`
	K8s      K8sDeploymentVerification  `yaml:"k8s,omitempty"`
}

type HTTPDeploymentVerification struct {
	URL          string `yaml:"url,omitempty"`
	JSONPointer  string `yaml:"json_pointer,omitempty"`
	AllowPrivate bool   `yaml:"allow_private,omitempty"`
}

type K8sDeploymentVerification struct {
	Cluster           string            `yaml:"cluster,omitempty"`
	Namespace         string            `yaml:"namespace,omitempty"`
	DeploymentsByRepo map[string]string `yaml:"deployments_by_repo,omitempty"`
	CommitAnnotation  string            `yaml:"commit_annotation,omitempty"`
	ImageLabel        string            `yaml:"image_label,omitempty"`
}

func (c DeploymentVerificationConfig) EffectiveProvider() string {
	if provider := strings.ToLower(strings.TrimSpace(c.Provider)); provider != "" {
		return provider
	}
	return DeploymentVerificationProviderManual
}

func (c DeploymentVerificationConfig) IsZero() bool {
	return strings.TrimSpace(c.Provider) == "" && c.HTTP.IsZero() && c.K8s.IsZero()
}

func (c HTTPDeploymentVerification) IsZero() bool {
	return strings.TrimSpace(c.URL) == "" && strings.TrimSpace(c.JSONPointer) == "" && !c.AllowPrivate
}

func (c K8sDeploymentVerification) IsZero() bool {
	return strings.TrimSpace(c.Cluster) == "" && strings.TrimSpace(c.Namespace) == "" &&
		len(c.DeploymentsByRepo) == 0 && strings.TrimSpace(c.CommitAnnotation) == "" &&
		strings.TrimSpace(c.ImageLabel) == ""
}

// DeploymentVerificationForEnvironment prevents automatic verification from
// silently falling back to another environment's runtime endpoint. New
// configurations do not need a deployment_verification block: when the
// environment already has K8s observability enabled, that read-only runtime is
// also the automatic deployment-evidence source.
func (c *SystemConfig) DeploymentVerificationForEnvironment(environment string) (DeploymentVerificationConfig, error) {
	if c == nil {
		return DeploymentVerificationConfig{}, fmt.Errorf("unknown environment %q", environment)
	}
	environment = strings.TrimSpace(environment)
	for _, configured := range c.Environments {
		if configured.ID == environment {
			resolved := configured.DeploymentVerification
			if resolved.IsZero() && c.Infrastructure.Observability.K8sRuntime.Enabled {
				resolved.Provider = DeploymentVerificationProviderK8s
			}
			if resolved.EffectiveProvider() == DeploymentVerificationProviderK8s {
				resolved.K8s = c.resolveK8sDeploymentVerification(environment, resolved.K8s)
			}
			return resolved, nil
		}
	}
	return DeploymentVerificationConfig{}, fmt.Errorf("unknown environment %q", environment)
}

// resolveK8sDeploymentVerification reuses the K8s runtime routing configured
// under observability. The legacy deployment_verification.k8s block remains a
// fallback for installed robots created before the wizard stopped emitting a
// second copy of cluster/namespace/workload mappings.
func (c *SystemConfig) resolveK8sDeploymentVerification(environment string, legacy K8sDeploymentVerification) K8sDeploymentVerification {
	resolved := legacy
	resolved.DeploymentsByRepo = make(map[string]string, len(legacy.DeploymentsByRepo))
	for repo, deployment := range legacy.DeploymentsByRepo {
		resolved.DeploymentsByRepo[repo] = deployment
	}
	entriesByService := map[string][]K8sRuntimeServiceMapEntry{}
	for _, entry := range c.Infrastructure.Observability.K8sRuntime.ServiceMap {
		if strings.TrimSpace(entry.Env) != environment || strings.TrimSpace(entry.Workload) == "" {
			continue
		}
		service := strings.TrimSpace(entry.Service)
		entriesByService[service] = append(entriesByService[service], entry)
	}
	derivedCluster, derivedNamespace := "", ""
	locationAmbiguous := false
	for _, repo := range c.Repos {
		services := append([]string(nil), repo.ServiceNames...)
		if len(services) == 0 {
			services = []string{repo.Name}
		}
		candidates := make([]K8sRuntimeServiceMapEntry, 0, len(services))
		seen := map[string]bool{}
		for _, service := range services {
			for _, entry := range entriesByService[strings.TrimSpace(service)] {
				identity := strings.Join([]string{strings.TrimSpace(entry.Cluster), strings.TrimSpace(entry.Namespace), strings.TrimSpace(entry.Workload)}, "\x1f")
				if !seen[identity] {
					seen[identity] = true
					candidates = append(candidates, entry)
				}
			}
		}
		// A repository may own multiple independently deployed services. The
		// current verifier accepts one Deployment per repository, so only derive
		// a mapping when observability identifies one unambiguous workload.
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) != 1 {
			delete(resolved.DeploymentsByRepo, repo.Name)
			locationAmbiguous = true
			continue
		}
		candidate := candidates[0]
		resolved.DeploymentsByRepo[repo.Name] = strings.TrimSpace(candidate.Workload)
		cluster := strings.TrimSpace(candidate.Cluster)
		namespace := strings.TrimSpace(candidate.Namespace)
		if cluster != "" {
			if derivedCluster != "" && derivedCluster != cluster {
				locationAmbiguous = true
			} else {
				derivedCluster = cluster
			}
		}
		if namespace != "" {
			if derivedNamespace != "" && derivedNamespace != namespace {
				locationAmbiguous = true
			} else {
				derivedNamespace = namespace
			}
		}
	}
	if locationAmbiguous {
		// The legacy verifier can query only one cluster/namespace per Case.
		// Never fall back to a possibly stale duplicate when observability says
		// the affected repositories span multiple runtime locations.
		resolved.Cluster = ""
		resolved.Namespace = ""
	} else {
		if derivedCluster != "" {
			resolved.Cluster = derivedCluster
		}
		if derivedNamespace != "" {
			resolved.Namespace = derivedNamespace
		}
	}
	return resolved
}
