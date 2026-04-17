package analyzer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanYAML_Nacos_HitsAllFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bootstrap.yaml")
	writeFile(t, p, `
nacos:
  server-addr: nacos:8848
  namespace-id: dev-ns
  group: MY_GROUP
  data-id: svc.yaml
`)
	f, err := ScanYAML(p, "bootstrap.yaml", CenterNacos)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if f == nil {
		t.Fatal("expected hit, got nil")
	}
	if f.DataID != "svc.yaml" || f.Group != "MY_GROUP" || f.NamespaceID != "dev-ns" || f.ServerAddr != "nacos:8848" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestScanYAML_Nacos_KeyVariants(t *testing.T) {
	cases := map[string]string{
		"hyphen":     "nacos:\n  data-id: v1",
		"underscore": "nacos:\n  data_id: v1",
		"camelCase":  "nacos:\n  dataId: v1",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "c.yaml")
			writeFile(t, p, yaml)
			f, err := ScanYAML(p, "c.yaml", CenterNacos)
			if err != nil {
				t.Fatal(err)
			}
			if f == nil || f.DataID != "v1" {
				t.Errorf("variant %s: expected data-id=v1, got %+v", name, f)
			}
		})
	}
}

func TestScanYAML_Apollo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "application.yml")
	writeFile(t, p, `
app:
  id: my-service
apollo:
  meta: http://apollo:8080
  bootstrap:
    namespaces: application,foo.yaml,bar
  cluster: prod
`)
	f, err := ScanYAML(p, "application.yml", CenterApollo)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected hit")
	}
	if f.AppID != "my-service" {
		t.Errorf("AppID: got %q", f.AppID)
	}
	if f.ServerAddr != "http://apollo:8080" {
		t.Errorf("ServerAddr: got %q", f.ServerAddr)
	}
	if !reflect.DeepEqual(f.Namespaces, []string{"application", "foo.yaml", "bar"}) {
		t.Errorf("Namespaces: got %v", f.Namespaces)
	}
	if f.Cluster != "prod" {
		t.Errorf("Cluster: got %q", f.Cluster)
	}
}

func TestScanYAML_Apollo_NamespacesArray(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bootstrap.yml")
	writeFile(t, p, `
apollo:
  bootstrap:
    namespaces:
      - application
      - datasource
`)
	f, err := ScanYAML(p, "bootstrap.yml", CenterApollo)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || !reflect.DeepEqual(f.Namespaces, []string{"application", "datasource"}) {
		t.Errorf("expected array parse, got %+v", f)
	}
}

func TestScanYAML_Consul(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bootstrap.yml")
	writeFile(t, p, `
spring:
  cloud:
    consul:
      host: consul-prod.internal
      config:
        prefix: config
        default-context: my-service
`)
	f, err := ScanYAML(p, "bootstrap.yml", CenterConsul)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected hit")
	}
	if f.KVPrefix != "config" || f.DefaultContext != "my-service" {
		t.Errorf("unexpected finding: %+v", f)
	}
	if f.ServerAddr != "consul-prod.internal" {
		t.Errorf("ServerAddr: got %q", f.ServerAddr)
	}
}

func TestScanYAML_NoHit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	writeFile(t, p, "server:\n  port: 8080\n")
	f, err := ScanYAML(p, "c.yaml", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Errorf("expected nil, got %+v", f)
	}
}

func TestScanYAML_Malformed_NoError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	writeFile(t, p, "key: [unclosed\n")
	f, err := ScanYAML(p, "bad.yaml", CenterNacos)
	if err != nil {
		t.Fatalf("should tolerate malformed yaml, got %v", err)
	}
	if f != nil {
		t.Errorf("expected nil from malformed file, got %+v", f)
	}
}
