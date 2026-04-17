package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPHPAnalyzer_FakeRepo(t *testing.T) {
	a := NewPHPAnalyzer(CenterNacos)
	ra, err := a.Analyze("../../examples/fake-repos/php-service", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	// composer.json → "shop/php-service" → service name "php-service"
	if len(ra.ServiceNames) != 1 || ra.ServiceNames[0] != "php-service" {
		t.Errorf("ServiceNames: got %v, want [php-service]", ra.ServiceNames)
	}
	// .env.example + .env.production → 2 findings
	if len(ra.Findings) < 2 {
		t.Fatalf("expected >= 2 findings (.env.example + .env.production), got %d", len(ra.Findings))
	}
	// 检查 .env.production 的 finding 有 prod profile + 正确字段
	var prodFinding *Finding
	for i := range ra.Findings {
		if ra.Findings[i].EnvProfile == "prod" {
			prodFinding = &ra.Findings[i]
			break
		}
	}
	if prodFinding == nil {
		t.Fatal("expected finding with EnvProfile=prod from .env.production")
	}
	if prodFinding.ServerAddr != "nacos-prod:8848" {
		t.Errorf("prod ServerAddr: got %q", prodFinding.ServerAddr)
	}
	if prodFinding.NamespaceID != "shop-prod" {
		t.Errorf("prod NamespaceID: got %q", prodFinding.NamespaceID)
	}
	if prodFinding.Group != "SHOP_PHP" {
		t.Errorf("prod Group: got %q", prodFinding.Group)
	}
	if prodFinding.DataID != "php-service.yaml" {
		t.Errorf("prod DataID: got %q", prodFinding.DataID)
	}
}

func TestPHPAnalyzer_NoConfigCenter(t *testing.T) {
	a := NewPHPAnalyzer("none")
	ra, err := a.Analyze("../../examples/fake-repos/php-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.ServiceNames) != 1 {
		t.Errorf("should still get name from composer.json, got %v", ra.ServiceNames)
	}
	if len(ra.Findings) != 0 {
		t.Errorf("config_center=none should not scan .env, got %d findings", len(ra.Findings))
	}
}

func TestReadComposerName(t *testing.T) {
	cases := map[string]string{
		`{"name": "shop/php-service"}`:    "php-service",
		`{"name": "php-service"}`:         "php-service",
		`{"name": "vendor/package-name"}`: "package-name",
		`{"name": ""}`:                    "",
		`{}`:                              "",
	}
	for input, want := range cases {
		dir := t.TempDir()
		p := filepath.Join(dir, "composer.json")
		if err := os.WriteFile(p, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		got := readComposerName(p)
		if got != want {
			t.Errorf("readComposerName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestScanDotEnv_Nacos(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	writeFile(t, p, `
APP_NAME=my-app
NACOS_ADDR=nacos:8848
NACOS_NAMESPACE=dev-ns
NACOS_GROUP=MY_GROUP
NACOS_DATA_ID=my-app.yaml
REDIS_HOST=redis:6379
`)
	f, err := ScanDotEnv(p, ".env", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected finding")
	}
	if f.ServerAddr != "nacos:8848" || f.NamespaceID != "dev-ns" || f.Group != "MY_GROUP" || f.DataID != "my-app.yaml" {
		t.Errorf("unexpected: %+v", f)
	}
	if f.EnvProfile != "" {
		t.Errorf("plain .env should not have profile, got %q", f.EnvProfile)
	}
}

func TestScanDotEnv_Apollo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env.development")
	writeFile(t, p, `
APP_ID=my-app
APOLLO_META=http://apollo:8080
APOLLO_CLUSTER=dev
APOLLO_NAMESPACE=application,datasource
`)
	f, err := ScanDotEnv(p, ".env.development", CenterApollo)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected finding")
	}
	if f.AppID != "my-app" || f.ServerAddr != "http://apollo:8080" || f.Cluster != "dev" {
		t.Errorf("unexpected: %+v", f)
	}
	if f.EnvProfile != "dev" {
		t.Errorf("expected profile=dev from .env.development, got %q", f.EnvProfile)
	}
}

func TestScanDotEnv_Consul(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env.production")
	writeFile(t, p, `
CONSUL_HTTP_ADDR=consul:8500
CONSUL_HTTP_TOKEN=secret-token
CONSUL_KV_PREFIX=config
`)
	f, err := ScanDotEnv(p, ".env.production", CenterConsul)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected finding")
	}
	if f.ServerAddr != "consul:8500" || f.KVPrefix != "config" {
		t.Errorf("unexpected: %+v", f)
	}
	if f.EnvProfile != "prod" {
		t.Errorf("expected profile=prod from .env.production, got %q", f.EnvProfile)
	}
}

func TestScanDotEnv_NoHit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	writeFile(t, p, "APP_NAME=test\nDB_HOST=localhost\n")
	f, err := ScanDotEnv(p, ".env", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Errorf("expected nil for .env without nacos keys, got %+v", f)
	}
}

func TestScanDotEnv_QuotedValues(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	writeFile(t, p, `NACOS_ADDR="nacos:8848"
NACOS_GROUP='MY_GROUP'
`)
	f, err := ScanDotEnv(p, ".env", CenterNacos)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.ServerAddr != "nacos:8848" || f.Group != "MY_GROUP" {
		t.Errorf("should strip quotes: %+v", f)
	}
}
