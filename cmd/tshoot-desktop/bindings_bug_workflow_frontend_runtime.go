package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

type caseFrontendRuntimeResolver struct{ app *App }

var conventionalFrontendRevisionKeys = []string{
	"org.opencontainers.image.revision",
	"app.kubernetes.io/version",
	"app.kubernetes.io/commit",
	"git-commit",
	"vcs-ref",
}

func (r caseFrontendRuntimeResolver) ResolveFrontendRuntime(ctx context.Context, incident bughub.IncidentCase) (bughub.FrontendRuntimeManifest, error) {
	manifest := bughub.FrontendRuntimeManifest{
		Environment:     incident.Environment,
		Precision:       bughub.FrontendPrecisionUnavailable,
		SourceMapStatus: "not_registered",
	}
	if r.app == nil {
		return manifest, errors.New("Studio workflow is unavailable")
	}
	loader := r.app.workflowLoadDeploymentConfig
	if loader == nil {
		loader = r.app.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil || cfg.System.ID != incident.SystemID {
		return manifest, errors.New("Case robot configuration is unavailable")
	}
	var environment *config.Environment
	for index := range cfg.Environments {
		if cfg.Environments[index].ID == incident.Environment {
			environment = &cfg.Environments[index]
			break
		}
	}
	if environment == nil {
		return manifest, fmt.Errorf("environment %q is not configured", incident.Environment)
	}

	runtimeRepos := make([]config.Repo, 0)
	for _, repo := range cfg.Repos {
		if repo.IsServiceNode() {
			runtimeRepos = append(runtimeRepos, repo)
		}
	}
	if len(runtimeRepos) == 0 {
		manifest.Limitations = append(manifest.Limitations, "no runtime repository is configured")
		return manifest, nil
	}

	k8sConfig := environment.DeploymentVerification.K8s
	var reader bughub.K8sDeploymentReader
	if environment.DeploymentVerification.EffectiveProvider() == config.DeploymentVerificationProviderK8s {
		if _, safe := k8sVerifierEndpointIdentity(cfg, environment.ID); safe {
			factory := r.app.workflowK8sReaderFactory
			if factory == nil {
				factory = r.app.newKuboardDeploymentReader
			}
			reader, _ = factory(ctx, cfg, *environment)
		} else {
			manifest.Limitations = append(manifest.Limitations, "K8s runtime endpoint is unavailable or unsafe")
		}
	} else {
		manifest.Limitations = append(manifest.Limitations, "environment does not use K8s deployment verification")
	}

	for _, configured := range runtimeRepos {
		repository := bughub.FrontendRuntimeRepository{
			Repo: configured.Name, Role: configured.EffectiveRole(), SubPath: configured.SubPath,
			Precision: bughub.FrontendPrecisionRepository,
		}
		repository.Deployment = strings.TrimSpace(k8sConfig.DeploymentsByRepo[configured.Name])
		if repository.Deployment == "" {
			repository.Limitations = append(repository.Limitations, "repository has no Deployment mapping")
			manifest.Repositories = append(manifest.Repositories, repository)
			manifest.Precision = betterFrontendPrecision(manifest.Precision, repository.Precision)
			continue
		}
		repository.Cluster = k8sConfig.Cluster
		repository.Namespace = k8sConfig.Namespace
		if reader == nil {
			repository.Limitations = append(repository.Limitations, "Deployment metadata could not be read")
			manifest.Repositories = append(manifest.Repositories, repository)
			manifest.Precision = betterFrontendPrecision(manifest.Precision, repository.Precision)
			continue
		}
		deployment, readErr := reader.ReadDeployment(ctx, k8sConfig.Cluster, k8sConfig.Namespace, repository.Deployment)
		if readErr != nil {
			repository.Limitations = append(repository.Limitations, "Deployment read failed")
			manifest.Repositories = append(manifest.Repositories, repository)
			manifest.Precision = betterFrontendPrecision(manifest.Precision, repository.Precision)
			continue
		}
		for _, image := range deployment.Images {
			if image = safeFrontendImage(image); image != "" {
				repository.Images = append(repository.Images, image)
			}
		}
		repository.Revision, repository.RevisionSource = frontendDeploymentRevision(k8sConfig, deployment)
		if repository.Revision != "" {
			repository.Precision = bughub.FrontendPrecisionDeployedRevision
		} else if revision, precision, source := frontendImageRevision(deployment.Images); revision != "" {
			repository.Revision = revision
			repository.RevisionSource = source
			repository.Precision = precision
			repository.Limitations = append(repository.Limitations, "image identity is a build candidate, not a verified source commit")
		} else {
			repository.Limitations = append(repository.Limitations, "Deployment exposes no usable revision, image digest, or stable image tag")
		}
		manifest.Repositories = append(manifest.Repositories, repository)
		manifest.Precision = betterFrontendPrecision(manifest.Precision, repository.Precision)
	}
	manifest.Limitations = append(manifest.Limitations, "source maps are discovered from browser evidence or build artifacts; exact frontend source file/line still requires a version-matched map")
	return manifest, nil
}

func frontendDeploymentRevision(cfg config.K8sDeploymentVerification, deployment bughub.K8sDeploymentVersion) (string, string) {
	if key := strings.TrimSpace(cfg.CommitAnnotation); key != "" {
		if value := safeFrontendRevision(deployment.Annotations[key]); value != "" {
			return value, "annotation:" + key
		}
	}
	if key := strings.TrimSpace(cfg.ImageLabel); key != "" {
		if value := safeFrontendRevision(deployment.Labels[key]); value != "" {
			return value, "label:" + key
		}
	}
	for _, key := range conventionalFrontendRevisionKeys {
		if value := safeFrontendRevision(deployment.Annotations[key]); value != "" {
			return value, "conventional_annotation:" + key
		}
		if value := safeFrontendRevision(deployment.Labels[key]); value != "" {
			return value, "conventional_label:" + key
		}
	}
	return "", ""
}

func safeFrontendRevision(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 240 || strings.ContainsAny(value, "\r\n\t ") {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "password") || strings.Contains(lower, "token=") || strings.Contains(lower, "authorization") {
		return ""
	}
	return value
}

func safeFrontendImage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 500 || strings.ContainsAny(value, "\r\n\t ") || strings.Contains(value, "://") {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "authorization", "token=", "secret="} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	return value
}

func frontendImageRevision(images []string) (string, string, string) {
	for _, image := range images {
		image = strings.TrimSpace(image)
		if marker := strings.LastIndex(image, "@sha256:"); marker >= 0 {
			digest := safeFrontendRevision(image[marker+1:])
			if digest != "" {
				return digest, bughub.FrontendPrecisionImageDigest, "image_digest"
			}
		}
	}
	for _, image := range images {
		image = strings.TrimSpace(image)
		slash := strings.LastIndex(image, "/")
		colon := strings.LastIndex(image, ":")
		if colon <= slash {
			continue
		}
		tag := safeFrontendRevision(image[colon+1:])
		if tag != "" && !strings.EqualFold(tag, "latest") {
			return tag, bughub.FrontendPrecisionImageTag, "image_tag"
		}
	}
	return "", "", ""
}

func betterFrontendPrecision(current, candidate string) string {
	rank := map[string]int{
		bughub.FrontendPrecisionUnavailable:      0,
		bughub.FrontendPrecisionRepository:       1,
		bughub.FrontendPrecisionImageTag:         2,
		bughub.FrontendPrecisionImageDigest:      3,
		bughub.FrontendPrecisionDeployedRevision: 4,
	}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}
