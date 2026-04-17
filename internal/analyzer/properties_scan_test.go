package analyzer

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanProperties_Nacos(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "application.properties")
	writeFile(t, p, `
# nacos config
spring.cloud.nacos.config.data-id=svc-dev.yaml
spring.cloud.nacos.config.group=DEV_GROUP
spring.cloud.nacos.config.namespace=dev-ns
spring.cloud.nacos.config.server-addr=nacos-dev:8848
`)
	f, err := ScanProperties(p, "application.properties", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.DataID != "svc-dev.yaml" || f.Group != "DEV_GROUP" || f.NamespaceID != "dev-ns" || f.ServerAddr != "nacos-dev:8848" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestScanProperties_Apollo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "application.properties")
	writeFile(t, p, `
app.id=my-app
apollo.meta=http://apollo.dev:8080
apollo.bootstrap.namespaces=application,foo.yaml,bar
apollo.cluster=dev
`)
	f, err := ScanProperties(p, "application.properties", CenterApollo)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected hit")
	}
	if f.AppID != "my-app" || f.Cluster != "dev" || f.ServerAddr != "http://apollo.dev:8080" {
		t.Errorf("unexpected finding: %+v", f)
	}
	if !reflect.DeepEqual(f.Namespaces, []string{"application", "foo.yaml", "bar"}) {
		t.Errorf("Namespaces: got %v", f.Namespaces)
	}
}

func TestScanProperties_Consul(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "application.properties")
	writeFile(t, p, `
spring.cloud.consul.host=consul.internal
spring.cloud.consul.config.prefix=kv
spring.cloud.consul.config.default-context=my-svc
`)
	f, err := ScanProperties(p, "application.properties", CenterConsul)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.KVPrefix != "kv" || f.DefaultContext != "my-svc" || f.ServerAddr != "consul.internal" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestScanProperties_IgnoresCommentsAndEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.properties")
	writeFile(t, p, `
# this is a comment

spring.cloud.nacos.config.data-id=x.yaml

# another comment
`)
	f, err := ScanProperties(p, "c.properties", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.DataID != "x.yaml" {
		t.Errorf("expected data-id=x.yaml, got %+v", f)
	}
}

func TestScanProperties_NoHit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.properties")
	writeFile(t, p, "server.port=8080\nlogging.level.root=INFO\n")
	f, err := ScanProperties(p, "c.properties", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Errorf("expected nil, got %+v", f)
	}
}
