package topology

type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

type Endpoint struct {
	ID         string      `json:"id" yaml:"id"`
	Repo       string      `json:"repo" yaml:"repo"`
	Service    string      `json:"service" yaml:"service"`
	Direction  Direction   `json:"direction" yaml:"direction"`
	Protocol   string      `json:"protocol" yaml:"protocol"`
	Method     string      `json:"method,omitempty" yaml:"method,omitempty"`
	Path       string      `json:"path,omitempty" yaml:"path,omitempty"`
	RPCMethod  string      `json:"rpc_method,omitempty" yaml:"rpc_method,omitempty"`
	TargetHint string      `json:"target_hint,omitempty" yaml:"target_hint,omitempty"`
	Location   string      `json:"location" yaml:"location"`
	Source     string      `json:"source" yaml:"source"`
	Transforms []Transform `json:"transforms,omitempty" yaml:"transforms,omitempty"`
}

type Transform struct {
	Kind string `json:"kind" yaml:"kind"`
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

type CandidateEdge struct {
	FromEndpoint string   `json:"from_endpoint" yaml:"from_endpoint"`
	ToEndpoint   string   `json:"to_endpoint" yaml:"to_endpoint"`
	FromService  string   `json:"from_service" yaml:"from_service"`
	ToService    string   `json:"to_service" yaml:"to_service"`
	Protocol     string   `json:"protocol" yaml:"protocol"`
	Method       string   `json:"method,omitempty" yaml:"method,omitempty"`
	Path         string   `json:"path,omitempty" yaml:"path,omitempty"`
	RPCMethod    string   `json:"rpc_method,omitempty" yaml:"rpc_method,omitempty"`
	Confidence   float64  `json:"confidence" yaml:"confidence"`
	Status       string   `json:"status" yaml:"status"`
	Reasons      []string `json:"reasons" yaml:"reasons"`
	Conflicts    []string `json:"conflicts,omitempty" yaml:"conflicts,omitempty"`
}

type RepositoryStatus struct {
	Repo          string `json:"repo" yaml:"repo"`
	State         string `json:"state" yaml:"state"`
	Error         string `json:"error,omitempty" yaml:"error,omitempty"`
	EndpointCount int    `json:"endpoint_count" yaml:"endpoint_count"`
}

type Snapshot struct {
	SchemaVersion string              `json:"schema_version" yaml:"schema_version"`
	Services      []ServiceDescriptor `json:"services" yaml:"services"`
	Endpoints     []Endpoint          `json:"endpoints" yaml:"endpoints"`
	Edges         []CandidateEdge     `json:"edges" yaml:"edges"`
	Repositories  []RepositoryStatus  `json:"repositories" yaml:"repositories"`
}
