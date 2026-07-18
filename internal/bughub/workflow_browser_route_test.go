package bughub

import (
	"encoding/json"
	"testing"
)

func TestCanonicalBrowserSecurityPolicyEncodesEmptyOriginCollectionsAsArrays(t *testing.T) {
	canonical := canonicalBrowserSecurityPolicy(BrowserSecurityPolicy{})
	for name, values := range map[string][]string{
		"allowed_origins":     canonical.AllowedOrigins,
		"application_origins": canonical.ApplicationOrigins,
		"start_origins":       canonical.StartOrigins,
		"private_origins":     canonical.PrivateOrigins,
		"auth_origins":        canonical.AuthOrigins,
	} {
		if values == nil {
			t.Fatalf("%s is nil, want an empty JSON array", name)
		}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"allowed_origins":[],"application_origins":[],"start_origins":[],"private_origins":[],"auth_origins":[],"is_prod":false}`
	if string(encoded) != want {
		t.Fatalf("canonical policy JSON = %s, want %s", encoded, want)
	}
}

func TestBrowserPolicyDigestBindsCanonicalApplicationAndStartOrigins(t *testing.T) {
	policy := BrowserSecurityPolicy{
		AllowedOrigins:     []string{"https://login.example.com", "https://app.example.com"},
		ApplicationOrigins: []string{"https://app.example.com", "https://app.example.com"},
		StartOrigins:       []string{"https://app.example.com"},
		AuthOrigins:        []string{"https://login.example.com"},
	}
	canonical := canonicalBrowserSecurityPolicy(policy)
	if len(canonical.ApplicationOrigins) != 1 || canonical.ApplicationOrigins[0] != "https://app.example.com" || len(canonical.StartOrigins) != 1 || canonical.StartOrigins[0] != "https://app.example.com" {
		t.Fatalf("canonical policy = %+v", canonical)
	}
	digest, err := browserPolicySHA256(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []BrowserSecurityPolicy{
		{AllowedOrigins: policy.AllowedOrigins, ApplicationOrigins: []string{"https://login.example.com"}, StartOrigins: policy.StartOrigins, AuthOrigins: policy.AuthOrigins},
		{AllowedOrigins: policy.AllowedOrigins, ApplicationOrigins: policy.ApplicationOrigins, StartOrigins: []string{"https://login.example.com"}, AuthOrigins: policy.AuthOrigins},
	} {
		changedDigest, err := browserPolicySHA256(changed)
		if err != nil {
			t.Fatal(err)
		}
		if changedDigest == digest {
			t.Fatalf("policy digest ignored application/start origin change: %+v", changed)
		}
	}
}
