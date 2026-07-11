package config

import "strings"

type ServiceTopology struct {
	Overrides []ServiceTopologyOverride `yaml:"overrides,omitempty" json:"overrides,omitempty"`
}

type ServiceTopologyOverride struct {
	Action      string `yaml:"action" json:"action"`
	FromService string `yaml:"from_service" json:"from_service"`
	ToService   string `yaml:"to_service" json:"to_service"`
	Protocol    string `yaml:"protocol" json:"protocol"`
	Method      string `yaml:"method,omitempty" json:"method,omitempty"`
	Path        string `yaml:"path,omitempty" json:"path,omitempty"`
	RPCMethod   string `yaml:"rpc_method,omitempty" json:"rpc_method,omitempty"`
}

func (o ServiceTopologyOverride) SemanticKey() string {
	return strings.Join([]string{o.FromService, o.ToService, o.Protocol, o.Method, o.Path, o.RPCMethod}, "\x1f")
}
