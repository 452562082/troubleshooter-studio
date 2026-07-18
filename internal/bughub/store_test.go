package bughub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreUpsertAndList(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	created := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)

	first := Bug{
		ID:        "zentao-1842",
		Source:    "zentao",
		SourceID:  "1842",
		Title:     "支付页提交后 500",
		Status:    "active",
		Severity:  "P1",
		Env:       "prod",
		CreatedAt: created,
		UpdatedAt: created,
	}
	if err := store.Upsert(first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}

	first.Title = "支付页提交后 500,用户无法完成付款"
	first.CreatedAt = time.Time{}
	first.UpdatedAt = updated
	if err := store.Upsert(first); err != nil {
		t.Fatalf("upsert updated: %v", err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Title != first.Title {
		t.Fatalf("title = %q, want %q", got[0].Title, first.Title)
	}
	if got[0].UpdatedAt.IsZero() {
		t.Fatal("updated_at was not preserved")
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Fatalf("created_at = %s, want %s", got[0].CreatedAt, created)
	}
}

func TestStoreRejectsEmptyID(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Upsert(Bug{Title: "missing id"}); err == nil {
		t.Fatal("Upsert accepted empty ID")
	}
}

func TestStoreListNormalizesLegacyZentaoHTMLSteps(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `[{
  "id": "zentao-577",
  "source": "zentao",
  "source_id": "577",
  "title": "PC端搜索结果页",
  "steps": "\u003cp\u003e[步骤]\u003c/p\u003e\u003col\u003e\u003cli\u003ePC端进入搜索页。\u003c/li\u003e\u003c/ol\u003e"
}]`
	if err := os.WriteFile(store.Path(), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Steps != "[步骤]\n- PC端进入搜索页。" {
		t.Fatalf("steps = %q", items[0].Steps)
	}

	got, ok, err := store.Get("zentao-577")
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if got.Steps != items[0].Steps {
		t.Fatalf("get steps = %q", got.Steps)
	}
}

func TestStorePruneStaleIDsArchivesBugAndKeepsHistory(t *testing.T) {
	store := NewStore(t.TempDir())
	bugs := []Bug{
		{ID: "zentao-keep", Source: "zentao", PlatformID: "zentao-main", Title: "keep"},
		{ID: "zentao-drop", Source: "zentao", PlatformID: "zentao-main", Title: "drop"},
		{ID: "zentao-other-platform", Source: "zentao", PlatformID: "zentao-other", Title: "other platform"},
		{ID: "tapd-drop", Source: "tapd", PlatformID: "zentao-main", Title: "other source"},
	}
	for _, bug := range bugs {
		if err := store.Upsert(bug); err != nil {
			t.Fatalf("Upsert %s: %v", bug.ID, err)
		}
	}

	prunedIDs, err := store.PruneStaleIDs("zentao", "zentao-main", []string{"zentao-keep"})
	if err != nil {
		t.Fatalf("PruneStaleIDs: %v", err)
	}
	if len(prunedIDs) != 1 || prunedIDs[0] != "zentao-drop" {
		t.Fatalf("prunedIDs = %+v", prunedIDs)
	}
	if pruned, err := store.PruneStale("zentao", "zentao-main", []string{"zentao-keep"}); err != nil || pruned != 0 {
		t.Fatalf("second PruneStale pruned=%d err=%v", pruned, err)
	}
	for _, id := range []string{"zentao-keep", "zentao-other-platform", "tapd-drop"} {
		if _, ok, err := store.Get(id); err != nil || !ok {
			t.Fatalf("Get %s ok=%v err=%v", id, ok, err)
		}
	}
	archived, ok, err := store.Get("zentao-drop")
	if err != nil || !ok {
		t.Fatalf("Get archived zentao-drop ok=%v err=%v", ok, err)
	}
	if archived.InboxState != BugInboxHistory || archived.ArchivedAt == nil || archived.ArchiveReason != BugArchiveNoLongerAssigned {
		t.Fatalf("archived bug = %+v", archived)
	}
	history, err := store.ListHistory()
	if err != nil || len(history) != 1 || history[0].ID != "zentao-drop" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if inbox, err := store.ListInbox(); err != nil || len(inbox) != 3 {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}

	reopened := Bug{
		ID: archived.ID, Source: archived.Source, PlatformID: archived.PlatformID,
		Title: archived.Title, Status: "active", UpdatedAt: time.Now().UTC().Add(time.Minute),
	}
	if err := store.Upsert(reopened); err != nil {
		t.Fatalf("reactivate archived bug: %v", err)
	}
	reactivated, ok, err := store.Get("zentao-drop")
	if err != nil || !ok || reactivated.InboxState != BugInboxActive || reactivated.ArchivedAt != nil || reactivated.ArchiveReason != "" {
		t.Fatalf("reactivated bug=%+v ok=%v err=%v", reactivated, ok, err)
	}
}

func TestStoreResolvedBugIsListedAsHistory(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Upsert(Bug{ID: "zentao-840", Source: "zentao", Title: "search", Status: "resolved"}); err != nil {
		t.Fatal(err)
	}
	if inbox, err := store.ListInbox(); err != nil || len(inbox) != 0 {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
	history, err := store.ListHistory()
	if err != nil || len(history) != 1 || history[0].ArchiveReason != BugArchiveSourceResolved {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestStorePathIsUnderRoot(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if got := store.Path(); got != filepath.Join(root, "bugs.json") {
		t.Fatalf("Path = %q", got)
	}
}
