package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadCodeIntelligenceConfig(t *testing.T, ci CodeIntelligence) *SystemConfig {
	t.Helper()
	c := minimalValid()
	c.CodeIntelligence = ci
	data, err := yaml.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCodeIntelligence_DefaultDisabled(t *testing.T) {
	cfg := loadCodeIntelligenceConfig(t, CodeIntelligence{})
	if cfg.CodeIntelligence.Enabled || cfg.CodeIntelligence.UsesCodeGraph() {
		t.Fatalf("zero value must be disabled: %#v", cfg.CodeIntelligence)
	}
}

func TestCodeIntelligence_CodeGraphEnabled(t *testing.T) {
	cfg := loadCodeIntelligenceConfig(t, CodeIntelligence{Enabled: true, Provider: "codegraph"})
	if !cfg.CodeIntelligence.UsesCodeGraph() {
		t.Fatalf("expected codegraph enabled: %#v", cfg.CodeIntelligence)
	}
}

func TestCodeIntelligence_RejectsMissingOrUnknownProvider(t *testing.T) {
	for _, provider := range []string{"", "lsp", "sourcegraph"} {
		c := minimalValid()
		c.CodeIntelligence = CodeIntelligence{Enabled: true, Provider: provider}
		err := Validate(&c)
		if err == nil || !strings.Contains(err.Error(), "code_intelligence.provider") {
			t.Fatalf("provider=%q err=%v", provider, err)
		}
	}
}

func TestHealthCheck_CodeIntelligenceSkillWasTrimmed(t *testing.T) {
	cfg := loadCodeIntelligenceConfig(t, CodeIntelligence{Enabled: true, Provider: "codegraph"})
	cfg.Generation.SkillsWhitelist = []string{"routing", "incident-investigator"}
	issues := HealthCheck(cfg)
	for _, issue := range issues {
		if issue.Category == "generation" && strings.Contains(issue.Message, "code-intelligence-query") {
			return
		}
	}
	t.Fatalf("missing trimmed-skill issue: %#v", issues)
}
