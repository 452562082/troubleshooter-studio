package analyzer

import (
	"reflect"
	"testing"
)

// Go analyzer 的集成测试：指向 examples/fake-repos/order-service
func TestGoAnalyzer_FakeRepo(t *testing.T) {
	a := NewGoAnalyzer(CenterNacos)
	ra, err := a.Analyze("../../examples/fake-repos/order-service", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !reflect.DeepEqual(ra.ServiceNames, []string{"order-service"}) {
		t.Errorf("ServiceNames: got %v", ra.ServiceNames)
	}
	if len(ra.Findings) != 1 {
		t.Fatalf("findings: expected 1, got %d", len(ra.Findings))
	}
	f := ra.Findings[0]
	if f.DataID != "order-service.yaml" || f.Group != "SHOP_ORDER" || f.NamespaceID != "shop-dev" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestGoAnalyzer_NoConfigCenter(t *testing.T) {
	a := NewGoAnalyzer("none")
	ra, err := a.Analyze("../../examples/fake-repos/order-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.ServiceNames) != 1 {
		t.Errorf("still expect service name from go.mod, got %v", ra.ServiceNames)
	}
	if len(ra.Findings) != 0 {
		t.Errorf("expected no findings when config center is none, got %d", len(ra.Findings))
	}
}

func TestReadGoModName(t *testing.T) {
	name := readGoModName("../../examples/fake-repos/order-service/go.mod")
	if name != "order-service" {
		t.Errorf("expected order-service, got %q", name)
	}
}
