package bughub

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// K8sDeploymentReader is deliberately read-only and narrow: providers cannot
// update, patch, exec, scale, or otherwise mutate a workload through it.
type K8sDeploymentReader interface {
	ReadDeployment(context.Context, string, string, string) (K8sDeploymentVersion, error)
}

type K8sDeploymentVersion struct {
	Annotations map[string]string
	Labels      map[string]string
	Images      []string
}

type K8sVersionVerifier struct {
	Environment string
	Config      config.K8sDeploymentVerification
	Reader      K8sDeploymentReader
}

var conventionalDeploymentRevisionKeys = []string{
	"org.opencontainers.image.revision",
	"app.kubernetes.io/commit",
	"git-commit",
	"vcs-ref",
}

func (v K8sVersionVerifier) Verify(ctx context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	observation := newRuntimeDeploymentObservation(request, "k8s")
	if normalizedDeploymentSource(request.Source) != "k8s" || v.Reader == nil {
		setDeploymentDiagnostic(&observation, "provider_unavailable", "K8s 版本验证不可用")
		return observation, ErrDeploymentVerifierUnavailable
	}
	if strings.TrimSpace(request.Environment) != strings.TrimSpace(v.Environment) {
		observation.Result = DeploymentResultMismatched
		setDeploymentDiagnostic(&observation, "environment_mismatch", "K8s 环境与 Case 不一致")
		return observation, nil
	}
	if strings.TrimSpace(v.Config.Cluster) == "" || strings.TrimSpace(v.Config.Namespace) == "" {
		setDeploymentDiagnostic(&observation, "runtime_location_unavailable", "K8s 运行时定位不唯一或不完整，已跳过版本采集")
		return observation, ErrDeploymentVerifierUnavailable
	}
	for repo := range request.ExpectedCommits {
		if strings.TrimSpace(v.Config.DeploymentsByRepo[repo]) == "" {
			setDeploymentDiagnostic(&observation, "deployment_mapping_missing", "仓库缺少 Deployment 映射")
			return observation, fmt.Errorf("%w: repository %q has no deployment mapping", ErrDeploymentVerifierUnavailable, repo)
		}
	}
	repos := make([]string, 0, len(request.ExpectedCommits))
	for repo := range request.ExpectedCommits {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	versions := make([]string, 0, len(repos))
	for _, repo := range repos {
		deployment, err := v.Reader.ReadDeployment(ctx, v.Config.Cluster, v.Config.Namespace, v.Config.DeploymentsByRepo[repo])
		if err != nil {
			setDeploymentDiagnostic(&observation, "k8s_read_failed", "K8s Deployment 暂不可读")
			return observation, nil
		}
		commit := ""
		explicitRevisionField := false
		if key := strings.TrimSpace(v.Config.CommitAnnotation); key != "" {
			explicitRevisionField = true
			commit = strings.TrimSpace(deployment.Annotations[key])
		} else if key := strings.TrimSpace(v.Config.ImageLabel); key != "" {
			explicitRevisionField = true
			commit = strings.TrimSpace(deployment.Labels[key])
		} else {
			commit = conventionalDeploymentRevision(deployment)
		}
		expected := strings.TrimSpace(request.ExpectedCommits[repo])
		if commitMatchesExpected(commit, expected) || imagesContainExpectedCommit(deployment.Images, expected) {
			// Persist the canonical expected commit. A runtime can expose a short
			// SHA or include it in an immutable image tag; either is sufficient to
			// identify the deployed merge commit without asking the user to type it.
			observation.ObservedCommits[repo] = expected
			versions = append(versions, repo+"="+expected)
		} else if explicitRevisionField && commit != "" {
			observation.Result = DeploymentResultMismatched
			setDeploymentDiagnostic(&observation, "commit_mismatch", "运行环境明确暴露的版本与期望提交不一致")
			return observation, nil
		}
		if len(deployment.Images) > 0 {
			observation.ObservedImages[repo] = strings.Join(deployment.Images, ",")
		}
	}
	observation.ObservedVersion = strings.Join(versions, ";")
	if len(observation.ObservedCommits) != len(observation.ExpectedCommits) {
		setDeploymentDiagnostic(&observation, "version_not_exposed", "运行环境未暴露可确认的版本信息，已跳过版本采集")
		return observation, nil
	}
	return finishExactRuntimeObservation(observation), nil
}

func conventionalDeploymentRevision(deployment K8sDeploymentVersion) string {
	for _, key := range conventionalDeploymentRevisionKeys {
		if value := strings.TrimSpace(deployment.Annotations[key]); value != "" {
			return value
		}
		if value := strings.TrimSpace(deployment.Labels[key]); value != "" {
			return value
		}
	}
	return ""
}

func commitMatchesExpected(observed, expected string) bool {
	observed = strings.ToLower(strings.TrimSpace(observed))
	expected = strings.ToLower(strings.TrimSpace(expected))
	if observed == "" || expected == "" {
		return false
	}
	if observed == expected {
		return true
	}
	return len(observed) >= 7 && strings.HasPrefix(expected, observed) || len(expected) >= 7 && strings.HasPrefix(observed, expected)
}

func imagesContainExpectedCommit(images []string, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) < 7 {
		return false
	}
	for _, image := range images {
		if strings.Contains(strings.ToLower(image), expected) {
			return true
		}
		// Images commonly use a shortened commit as the immutable tag.
		for length := len(expected); length >= 7; length-- {
			if strings.Contains(strings.ToLower(image), expected[:length]) {
				return true
			}
		}
	}
	return false
}

func newRuntimeDeploymentObservation(request DeploymentVerificationRequest, source string) DeploymentObservation {
	return DeploymentObservation{
		Environment:        strings.TrimSpace(request.Environment),
		ExpectedCommits:    CloneStringMap(request.ExpectedCommits),
		VerificationSource: source,
		ObservedImages:     map[string]string{},
		ObservedCommits:    map[string]string{},
		ObservedAt:         time.Now().UTC(),
		Result:             DeploymentResultUnavailable,
	}
}

func finishExactRuntimeObservation(observation DeploymentObservation) DeploymentObservation {
	if len(observation.ExpectedCommits) == 0 {
		observation.Result = DeploymentResultUnavailable
		setDeploymentDiagnostic(&observation, "expected_scope_missing", "缺少待验证的仓库提交")
		return observation
	}
	for repo, expected := range observation.ExpectedCommits {
		if observation.ObservedCommits[repo] != expected {
			observation.Result = DeploymentResultMismatched
			setDeploymentDiagnostic(&observation, "commit_mismatch", "运行版本与期望提交不一致")
			return observation
		}
	}
	now := time.Now().UTC()
	observation.VerifiedAt = &now
	observation.Result = DeploymentResultMatched
	observation.DiagnosticCode = ""
	observation.DiagnosticMessage = ""
	return observation
}

func setDeploymentDiagnostic(observation *DeploymentObservation, code, message string) {
	if observation == nil {
		return
	}
	observation.DiagnosticCode = code
	observation.DiagnosticMessage = message
}
