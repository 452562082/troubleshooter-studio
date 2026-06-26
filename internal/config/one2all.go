package config

import "strings"

// UsesOne2All returns true when this agent needs the shared one2all streamable-http MCP.
func (c *SystemConfig) UsesOne2All() bool {
	for _, cc := range c.Infrastructure.ConfigCenters {
		if strings.EqualFold(strings.TrimSpace(cc.Type), "one2all") {
			return true
		}
	}
	k8s := c.Infrastructure.Observability.K8sRuntime
	return k8s.Enabled && strings.EqualFold(strings.TrimSpace(k8s.Provider), "one2all")
}
