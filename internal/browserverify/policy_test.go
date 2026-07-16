package browserverify

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type lookupResult struct {
	addresses []net.IPAddr
	err       error
}

type fakeResolver struct {
	results map[string][]lookupResult
	calls   map[string]int
}

func (r *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	call := r.calls[host]
	r.calls[host] = call + 1
	results := r.results[host]
	if len(results) == 0 {
		return nil, nil
	}
	if call >= len(results) {
		call = len(results) - 1
	}
	return results[call].addresses, results[call].err
}

func publicResolver(hosts ...string) *fakeResolver {
	results := make(map[string][]lookupResult, len(hosts))
	for _, host := range hosts {
		results[host] = []lookupResult{{addresses: []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}}}
	}
	return &fakeResolver{results: results}
}

func TestAllowedURLNormalizesConfiguredOriginUnion(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"HTTPS://APP.Example.COM", "https://API.example.com:8443/base"},
		AuthOrigins:    []string{"https://LOGIN.Example.COM:443"},
	}
	resolver := publicResolver("app.example.com", "api.example.com", "login.example.com")
	for _, rawURL := range []string{
		"https://app.example.com:443/users",
		"https://api.EXAMPLE.com:8443/v1/users",
		"https://login.example.com/sign-in",
	} {
		if err := AllowedURL(context.Background(), resolver, policy, rawURL); err != nil {
			t.Fatalf("AllowedURL(%q) error = %v", rawURL, err)
		}
	}
}

func TestAllowedURLRejectsInvalidDestinations(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}}
	for _, rawURL := range []string{
		"", "/users", "users", "file:///etc/passwd", "data:text/plain,secret",
		"javascript:alert(1)", "about:blank", "chrome://settings", "https:///users",
		"https://user:pass@app.example.com/users", "https://@app.example.com/users",
		"https://app.example.com:0/users", "https://app.example.com:65536/users",
		"https://app.example.com:not-a-port/users",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := AllowedURL(context.Background(), publicResolver("app.example.com"), policy, rawURL); !errors.Is(err, ErrBrowserDestinationBlocked) {
				t.Fatalf("error = %v, want ErrBrowserDestinationBlocked", err)
			}
		})
	}
}

func TestAllowedURLRejectsUnconfiguredOrigin(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}}
	resolver := publicResolver("evil.example.com", "app.example.com")
	for _, rawURL := range []string{
		"https://evil.example.com/redirect",
		"http://app.example.com/downgrade",
		"https://app.example.com:8443/other-port",
	} {
		if err := AllowedURL(context.Background(), resolver, policy, rawURL); !errors.Is(err, ErrBrowserOriginNotAllowed) {
			t.Fatalf("AllowedURL(%q) error = %v, want ErrBrowserOriginNotAllowed", rawURL, err)
		}
	}
}

func TestAllowedURLFailsClosedOnDNSFailureOrEmptyResult(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}}
	for name, result := range map[string]lookupResult{
		"dns error":    {err: errors.New("temporary DNS failure")},
		"empty result": {},
	} {
		t.Run(name, func(t *testing.T) {
			resolver := &fakeResolver{results: map[string][]lookupResult{"app.example.com": {result}}}
			if err := AllowedURL(context.Background(), resolver, policy, "https://app.example.com/users"); !errors.Is(err, ErrBrowserDestinationBlocked) {
				t.Fatalf("error = %v, want ErrBrowserDestinationBlocked", err)
			}
		})
	}
}

func TestAllowedURLRejectsEveryForbiddenResolvedAddress(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"https://app.example.com"},
		PrivateOrigins: []string{"https://app.example.com"},
	}
	for _, address := range []string{
		"169.254.169.254", "169.254.170.2", "100.100.100.200", "0.0.0.0", "::",
		"224.0.0.1", "ff02::1", "169.254.1.1", "fe80::1", "::ffff:169.254.169.254",
		"fd00:ec2::254",
	} {
		t.Run(address, func(t *testing.T) {
			resolver := &fakeResolver{results: map[string][]lookupResult{
				"app.example.com": {{addresses: []net.IPAddr{{IP: net.ParseIP(address)}}}},
			}}
			if err := AllowedURL(context.Background(), resolver, policy, "https://app.example.com/users"); !errors.Is(err, ErrBrowserDestinationBlocked) {
				t.Fatalf("error = %v, want ErrBrowserDestinationBlocked", err)
			}
		})
	}

	t.Run("one forbidden answer blocks the whole resolution", func(t *testing.T) {
		resolver := &fakeResolver{results: map[string][]lookupResult{
			"app.example.com": {{addresses: []net.IPAddr{
				{IP: net.ParseIP("203.0.113.10")},
				{IP: net.ParseIP("169.254.169.254")},
			}}},
		}}
		if err := AllowedURL(context.Background(), resolver, policy, "https://app.example.com/users"); !errors.Is(err, ErrBrowserDestinationBlocked) {
			t.Fatalf("error = %v, want ErrBrowserDestinationBlocked", err)
		}
	})
}

func TestAllowedURLRequiresExactPrivateOriginOptIn(t *testing.T) {
	cases := []struct {
		name    string
		address string
	}{
		{name: "loopback IPv4", address: "127.0.0.1"},
		{name: "RFC1918", address: "10.0.0.8"},
		{name: "unique local IPv6", address: "fd12:3456::8"},
		{name: "mapped loopback", address: "::ffff:127.0.0.1"},
		{name: "mapped RFC1918", address: "::ffff:192.168.1.8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeResolver{results: map[string][]lookupResult{
				"app.internal": {{addresses: []net.IPAddr{{IP: net.ParseIP(tc.address)}}}},
			}}
			base := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.internal:8443"}}
			if err := AllowedURL(context.Background(), resolver, base, "https://app.internal:8443/users"); !errors.Is(err, ErrBrowserDestinationBlocked) {
				t.Fatalf("without opt-in error = %v", err)
			}
			base.PrivateOrigins = []string{"HTTPS://APP.INTERNAL:8443"}
			if err := AllowedURL(context.Background(), resolver, base, "https://app.internal:8443/users"); err != nil {
				t.Fatalf("with exact opt-in error = %v", err)
			}
			base.PrivateOrigins = []string{"https://app.internal"}
			if err := AllowedURL(context.Background(), resolver, base, "https://app.internal:8443/users"); !errors.Is(err, ErrBrowserDestinationBlocked) {
				t.Fatalf("different-port opt-in error = %v", err)
			}
		})
	}

	t.Run("private origin must also be allowed", func(t *testing.T) {
		policy := bughub.BrowserSecurityPolicy{PrivateOrigins: []string{"https://app.internal"}}
		resolver := &fakeResolver{results: map[string][]lookupResult{
			"app.internal": {{addresses: []net.IPAddr{{IP: net.ParseIP("10.0.0.8")}}}},
		}}
		if err := AllowedURL(context.Background(), resolver, policy, "https://app.internal/users"); !errors.Is(err, ErrBrowserOriginNotAllowed) {
			t.Fatalf("error = %v, want ErrBrowserOriginNotAllowed", err)
		}
	})
}

func TestAllowedURLRejectsMetadataHostnamesBeforeDNS(t *testing.T) {
	for _, host := range []string{
		"metadata",
		"metadata.google.internal",
		"metadata.google.internal.",
		"instance-data.ec2.internal",
		"metadata.tencentyun.com",
	} {
		t.Run(host, func(t *testing.T) {
			origin := "https://" + host
			resolver := publicResolver(host)
			policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{origin}, PrivateOrigins: []string{origin}}
			if err := AllowedURL(context.Background(), resolver, policy, origin+"/latest"); !errors.Is(err, ErrBrowserDestinationBlocked) {
				t.Fatalf("error = %v, want ErrBrowserDestinationBlocked", err)
			}
		})
	}
}

func TestAllowedURLReparsesAndResolvesEveryCall(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}}
	resolver := &fakeResolver{results: map[string][]lookupResult{
		"app.example.com": {
			{addresses: []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}},
			{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		},
	}}
	if err := AllowedURL(context.Background(), resolver, policy, "https://app.example.com/start"); err != nil {
		t.Fatalf("first call error = %v", err)
	}
	if err := AllowedURL(context.Background(), resolver, policy, "https://app.example.com/redirect"); !errors.Is(err, ErrBrowserDestinationBlocked) {
		t.Fatalf("second call error = %v, want ErrBrowserDestinationBlocked", err)
	}
	if resolver.calls["app.example.com"] != 2 {
		t.Fatalf("DNS calls = %d, want 2", resolver.calls["app.example.com"])
	}
}

func TestValidatePlanRejectsProdInteractions(t *testing.T) {
	for _, action := range []string{"click", "fill", "press", "select"} {
		t.Run(action, func(t *testing.T) {
			policy := bughub.BrowserSecurityPolicy{IsProd: true, AllowedOrigins: []string{"https://app.example.com"}, ApplicationOrigins: []string{"https://app.example.com"}, StartOrigins: []string{"https://app.example.com"}}
			plan := bughub.BrowserPlan{
				Version:  1,
				StartURL: "https://app.example.com",
				Actions:  []bughub.BrowserAction{{ID: "restricted", Action: action}},
			}
			if err := ValidatePlan(context.Background(), publicResolver("app.example.com"), policy, plan); !errors.Is(err, ErrBrowserProdInteractionBlocked) {
				t.Fatalf("error = %v, want ErrBrowserProdInteractionBlocked", err)
			}
		})
	}
}

func TestValidatePlanAllowsOnlyReadOnlyProdActionsAndRechecksGoto(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{IsProd: true, AllowedOrigins: []string{"https://app.example.com"}, ApplicationOrigins: []string{"https://app.example.com"}, StartOrigins: []string{"https://app.example.com"}}
	plan := bughub.BrowserPlan{
		Version:  1,
		StartURL: "https://app.example.com/start",
		Actions: []bughub.BrowserAction{
			{ID: "navigate", Action: "goto", URL: "https://app.example.com/users"},
			{ID: "wait", Action: "wait_for"},
			{ID: "capture", Action: "screenshot"},
		},
	}
	resolver := publicResolver("app.example.com")
	if err := ValidatePlan(context.Background(), resolver, policy, plan); err != nil {
		t.Fatalf("ValidatePlan error = %v", err)
	}
	if resolver.calls["app.example.com"] != 2 {
		t.Fatalf("DNS calls = %d, want start_url and goto checks", resolver.calls["app.example.com"])
	}
}

func TestValidatePlanChecksEveryGotoOrigin(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}, ApplicationOrigins: []string{"https://app.example.com"}, StartOrigins: []string{"https://app.example.com"}}
	plan := bughub.BrowserPlan{
		Version:  1,
		StartURL: "https://app.example.com/start",
		Actions: []bughub.BrowserAction{
			{ID: "redirect", Action: "goto", URL: "https://evil.example.com/users"},
		},
	}
	if err := ValidatePlan(context.Background(), publicResolver("app.example.com", "evil.example.com"), policy, plan); !errors.Is(err, ErrBrowserOriginNotAllowed) {
		t.Fatalf("error = %v, want ErrBrowserOriginNotAllowed", err)
	}
}

func TestValidatePlanRejectsAPIAndAuthenticationOriginsAsStartOwners(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins:     []string{"https://app.example.com", "https://api.example.com", "https://login.example.com"},
		ApplicationOrigins: []string{"https://app.example.com"},
		StartOrigins:       []string{"https://app.example.com"},
		AuthOrigins:        []string{"https://login.example.com"},
	}
	for _, startURL := range []string{"https://api.example.com/v1", "https://login.example.com/sso"} {
		plan := bughub.BrowserPlan{Version: 1, StartURL: startURL, Actions: []bughub.BrowserAction{{ID: "capture", Action: "screenshot"}}}
		resolver := publicResolver("app.example.com", "api.example.com", "login.example.com")
		if err := ValidatePlan(context.Background(), resolver, policy, plan); !errors.Is(err, ErrBrowserOriginNotAllowed) {
			t.Fatalf("start URL %q error = %v, want ErrBrowserOriginNotAllowed", startURL, err)
		}
		if len(resolver.calls) != 0 {
			t.Fatalf("start URL %q reached DNS before start-origin rejection: %+v", startURL, resolver.calls)
		}
	}
}

func TestValidatePlanLeavesNonProdActionSchemaToParser(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}, ApplicationOrigins: []string{"https://app.example.com"}, StartOrigins: []string{"https://app.example.com"}}
	plan := bughub.BrowserPlan{
		Version:  1,
		StartURL: "https://app.example.com/start",
		Actions:  []bughub.BrowserAction{{ID: "parser-owned", Action: "future_action"}},
	}
	if err := ValidatePlan(context.Background(), publicResolver("app.example.com"), policy, plan); err != nil {
		t.Fatalf("ValidatePlan reimplemented parser validation: %v", err)
	}
}
