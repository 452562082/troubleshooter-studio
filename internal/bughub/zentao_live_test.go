package bughub

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLiveZentaoSyncExtractsAttachments(t *testing.T) {
	if os.Getenv("ZENTAO_LIVE") != "1" {
		t.Skip("set ZENTAO_LIVE=1 to run against the locally configured Zentao platform")
	}
	data, err := os.ReadFile(NewPlatformStore(DefaultRoot()).Path())
	if err != nil {
		t.Fatalf("read platforms: %v", err)
	}
	var platforms []PlatformConfig
	if err := json.Unmarshal(data, &platforms); err != nil {
		t.Fatalf("decode platforms: %v", err)
	}
	if len(platforms) == 0 {
		t.Fatal("no configured bug platforms")
	}
	store := NewStore(t.TempDir())
	result, err := SyncZentaoAssigned(platforms[0], store, nil)
	if err != nil {
		t.Fatalf("SyncZentaoAssigned: %v", err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := map[string]int{}
	for _, item := range items {
		found[item.SourceID] = len(item.Attachments)
	}
	t.Logf("sync result: %+v attachments: %+v", result, found)
	if found["577"] == 0 && found["718"] == 0 {
		t.Fatalf("expected attachments for bug 577 or 718, got %+v", found)
	}
}
