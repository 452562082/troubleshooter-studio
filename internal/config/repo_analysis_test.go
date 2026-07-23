package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRepoAnalysisEnabledIsTriState(t *testing.T) {
	var document struct {
		Repos []Repo `yaml:"repos"`
	}
	if err := yaml.Unmarshal([]byte(`repos:
  - name: omitted
  - name: enabled
    analysis:
      enabled: true
  - name: disabled
    analysis:
      enabled: false
`), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Repos) != 3 {
		t.Fatalf("repos=%+v", document.Repos)
	}

	want := map[string]bool{"omitted": true, "enabled": true, "disabled": false}
	for _, repository := range document.Repos {
		if got := repository.Analysis.IsEnabled(); got != want[repository.Name] {
			t.Errorf("repository %q enabled=%v, want %v", repository.Name, got, want[repository.Name])
		}
	}
}
