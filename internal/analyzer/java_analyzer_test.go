package analyzer

import (
	"reflect"
	"sort"
	"testing"
)

func TestJavaAnalyzer_Nacos_FakeRepo(t *testing.T) {
	a := NewJavaAnalyzer(CenterNacos)
	ra, err := a.Analyze("../../examples/fake-repos/product-service", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !reflect.DeepEqual(ra.ServiceNames, []string{"product-service"}) {
		t.Errorf("ServiceNames: got %v", ra.ServiceNames)
	}
	if len(ra.Findings) != 2 {
		t.Fatalf("findings: expected 2 (bootstrap.yml + bootstrap-dev.yml), got %d", len(ra.Findings))
	}

	sort.Slice(ra.Findings, func(i, j int) bool {
		return ra.Findings[i].EnvProfile < ra.Findings[j].EnvProfile
	})

	// first: default profile (EnvProfile "")
	def := ra.Findings[0]
	if def.EnvProfile != "" || def.NamespaceID != "shop-prod-ns" || def.DataID != "product-service.yaml" {
		t.Errorf("default profile finding unexpected: %+v", def)
	}
	// second: dev profile
	dev := ra.Findings[1]
	if dev.EnvProfile != "dev" || dev.NamespaceID != "shop-dev-ns" || dev.DataID != "product-service-dev.yaml" {
		t.Errorf("dev profile finding unexpected: %+v", dev)
	}
}

func TestJavaAnalyzer_Apollo_YAMLAndProperties(t *testing.T) {
	a := NewJavaAnalyzer(CenterApollo)
	ra, err := a.Analyze("../../examples/fake-repos-apollo/account-service", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !reflect.DeepEqual(ra.ServiceNames, []string{"account-service"}) {
		t.Errorf("ServiceNames: got %v", ra.ServiceNames)
	}
	if len(ra.Findings) != 2 {
		t.Fatalf("findings: expected 2, got %d: %+v", len(ra.Findings), ra.Findings)
	}
	// 两个 finding 至少都有 AppID（验证之前 yaml Apollo 漏抽取的 bug 已修）
	for _, f := range ra.Findings {
		if f.AppID != "account-service" {
			t.Errorf("AppID should be account-service, got %q in %s", f.AppID, f.SourceFile)
		}
	}
}

func TestJavaAnalyzer_Consul(t *testing.T) {
	a := NewJavaAnalyzer(CenterConsul)
	ra, err := a.Analyze("../../examples/fake-repos-consul/device-gateway", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(ra.Findings))
	}
	f := ra.Findings[0]
	if f.KVPrefix != "config" || f.DefaultContext != "device-gateway" {
		t.Errorf("unexpected: %+v", f)
	}
}

func TestReadPomArtifactID(t *testing.T) {
	id := readPomArtifactID("../../examples/fake-repos/product-service/pom.xml")
	if id != "product-service" {
		t.Errorf("expected product-service, got %q", id)
	}
}

func TestExtractEnvProfile(t *testing.T) {
	cases := map[string]string{
		"bootstrap-dev.yml":           "dev",
		"application-prod.properties": "prod",
		"application.yml":             "",
		"BOOTSTRAP-STAGING.YML":       "staging",
	}
	for name, want := range cases {
		got := extractEnvProfile(name)
		if got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}
