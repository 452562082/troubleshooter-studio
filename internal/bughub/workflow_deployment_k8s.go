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

func (v K8sVersionVerifier) Verify(ctx context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	observation := newRuntimeDeploymentObservation(request, "k8s")
	if normalizedDeploymentSource(request.Source) != "k8s" || v.Reader == nil {
		return observation, ErrDeploymentVerifierUnavailable
	}
	if strings.TrimSpace(request.Environment) != strings.TrimSpace(v.Environment) {
		observation.Result = DeploymentResultMismatched
		return observation, nil
	}
	for repo := range request.ExpectedCommits {
		if strings.TrimSpace(v.Config.DeploymentsByRepo[repo]) == "" {
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
			return observation, nil
		}
		commit := ""
		if key := strings.TrimSpace(v.Config.CommitAnnotation); key != "" {
			commit = strings.TrimSpace(deployment.Annotations[key])
		} else if key := strings.TrimSpace(v.Config.ImageLabel); key != "" {
			commit = strings.TrimSpace(deployment.Labels[key])
		}
		if commit != "" {
			observation.ObservedCommits[repo] = commit
			versions = append(versions, repo+"="+commit)
		}
		if len(deployment.Images) > 0 {
			observation.ObservedImages[repo] = strings.Join(deployment.Images, ",")
		}
	}
	observation.ObservedVersion = strings.Join(versions, ";")
	return finishExactRuntimeObservation(observation), nil
}

func newRuntimeDeploymentObservation(request DeploymentVerificationRequest, source string) DeploymentObservation {
	return DeploymentObservation{
		Environment:        strings.TrimSpace(request.Environment),
		ExpectedCommits:    CloneStringMap(request.ExpectedCommits),
		VerificationSource: source,
		ObservedImages:     map[string]string{},
		ObservedCommits:    map[string]string{},
		Result:             DeploymentResultUnavailable,
	}
}

func finishExactRuntimeObservation(observation DeploymentObservation) DeploymentObservation {
	if len(observation.ExpectedCommits) == 0 {
		observation.Result = DeploymentResultUnavailable
		return observation
	}
	for repo, expected := range observation.ExpectedCommits {
		if observation.ObservedCommits[repo] != expected {
			observation.Result = DeploymentResultMismatched
			return observation
		}
	}
	now := time.Now().UTC()
	observation.VerifiedAt = &now
	observation.Result = DeploymentResultMatched
	return observation
}
