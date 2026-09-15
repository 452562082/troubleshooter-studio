package bughub

import "context"

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
