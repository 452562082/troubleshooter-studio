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
	URL         string `yaml:"url,omitempty"`
	JSONPointer string `yaml:"json_pointer,omitempty"`
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
	return strings.TrimSpace(c.URL) == "" && strings.TrimSpace(c.JSONPointer) == ""
}

func (c K8sDeploymentVerification) IsZero() bool {
	return strings.TrimSpace(c.Cluster) == "" && strings.TrimSpace(c.Namespace) == "" &&
		len(c.DeploymentsByRepo) == 0 && strings.TrimSpace(c.CommitAnnotation) == "" &&
		strings.TrimSpace(c.ImageLabel) == ""
}

// DeploymentVerificationForEnvironment prevents automatic verification from
// silently falling back to another environment's runtime endpoint.
func (c *SystemConfig) DeploymentVerificationForEnvironment(environment string) (DeploymentVerificationConfig, error) {
	if c == nil {
		return DeploymentVerificationConfig{}, fmt.Errorf("unknown environment %q", environment)
	}
	environment = strings.TrimSpace(environment)
	for _, configured := range c.Environments {
		if configured.ID == environment {
			return configured.DeploymentVerification, nil
		}
	}
	return DeploymentVerificationConfig{}, fmt.Errorf("unknown environment %q", environment)
}
