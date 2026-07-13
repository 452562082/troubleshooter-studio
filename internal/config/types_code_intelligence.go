package config

const CodeIntelligenceProviderCodeGraph = "codegraph"

type CodeIntelligence struct {
	Enabled  bool   `yaml:"enabled"`
	Provider string `yaml:"provider,omitempty"`
}

func (c CodeIntelligence) UsesCodeGraph() bool {
	return c.Enabled && c.Provider == CodeIntelligenceProviderCodeGraph
}
