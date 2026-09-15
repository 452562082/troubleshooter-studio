package config

import "testing"

func TestEffectiveFrontendEntriesKeepsLegacyWebDomain(t *testing.T) {
	environment := Environment{WebDomain: "https://web.test"}
	entries := environment.EffectiveFrontendEntries()
	if len(entries) != 1 || entries[0].ID != "default-web" || entries[0].URL != environment.WebDomain {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestEffectiveFrontendEntriesPrefersNamedApplications(t *testing.T) {
	environment := Environment{
		WebDomain:       "https://legacy.test",
		FrontendEntries: []FrontendEntry{{ID: "consumer", Name: "C 端", URL: "https://m.test"}, {ID: "admin", Name: "管理端", URL: "https://admin.test"}},
	}
	entries := environment.EffectiveFrontendEntries()
	if len(entries) != 3 || entries[0].ID != "consumer" || entries[2].ID != "default-web" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestEffectiveFrontendEntriesDeduplicatesSchemalessLegacyDomain(t *testing.T) {
	environment := Environment{
		WebDomain:       "m.test",
		FrontendEntries: []FrontendEntry{{ID: "consumer", Name: "C 端", URL: "https://m.test/"}},
	}
	entries := environment.EffectiveFrontendEntries()
	if len(entries) != 1 || entries[0].ID != "consumer" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestValidateFrontendEntryURLAllowsStablePathButRejectsQuery(t *testing.T) {
	if err := validateFrontendEntryURL("https://admin.test/console"); err != nil {
		t.Fatal(err)
	}
	if err := validateFrontendEntryURL("https://admin.test/console?token=x"); err == nil {
		t.Fatal("expected query URL to be rejected")
	}
}
