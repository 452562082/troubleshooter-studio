package bughub

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestHTTPVersionVerifierMatchesRFC6901Pointer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Authorization", "secret-response-header")
		_, _ = w.Write([]byte(`{"git":{"a/b":{"~commit":"abc123"}}}`))
	}))
	defer server.Close()
	v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/git/a~1b/~0commit", AllowPrivate: true}}
	got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"admin-web": "abc123"}})
	if err != nil || got.Result != DeploymentResultMatched || got.ObservedCommits["admin-web"] != "abc123" || got.VerifiedAt == nil {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
	if strings.Contains(got.ObservedVersion, "secret-response-header") {
		t.Fatal("response headers leaked")
	}
}

func TestHTTPVersionVerifierBlocksPrivateTargetsUnlessExplicitlyAllowed(t *testing.T) {
	t.Run("localhost denied by default", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte(`{"commit":"abc"}`)) }))
		defer server.Close()
		got, err := (HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit"}}).Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "abc"}})
		if err != nil || got.Result != DeploymentResultUnavailable || calls != 0 {
			t.Fatalf("got=%+v calls=%d err=%v", got, calls, err)
		}
	})
	t.Run("exact configured private host explicitly allowed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"commit":"abc"}`)) }))
		defer server.Close()
		got, err := (HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit", AllowPrivate: true}}).Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "abc"}})
		if err != nil || got.Result != DeploymentResultMatched {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	for _, tc := range []struct {
		rawURL       string
		allowPrivate bool
	}{{"http://169.254.169.254/latest/meta-data", true}, {"http://169.254.170.2/credentials", true}, {"http://100.100.100.200/latest/meta-data", true}, {"http://[::ffff:169.254.169.254]/latest/meta-data", true}, {"http://[fd00:ec2::254]/latest/meta-data", true}, {"http://127.0.0.1/version", false}, {"http://10.0.0.1/version", false}} {
		t.Run(tc.rawURL, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not dial") })}
			got, err := (HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: tc.rawURL, JSONPointer: "/commit", AllowPrivate: tc.allowPrivate}, Client: client}).Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "abc"}})
			if err != nil || got.Result != DeploymentResultUnavailable || calls != 0 {
				t.Fatalf("got=%+v calls=%d err=%v", got, calls, err)
			}
		})
	}
}

func TestHTTPVersionVerifierDialPolicyRechecksResolvedAddress(t *testing.T) {
	for _, tc := range []struct {
		name         string
		address      string
		allowPrivate bool
		wantDial     bool
	}{
		{name: "private rejected", address: "127.0.0.1:80"},
		{name: "private explicitly allowed", address: "127.0.0.1:80", allowPrivate: true, wantDial: true},
		{name: "metadata always rejected", address: "169.254.169.254:80", allowPrivate: true},
		{name: "container credentials always rejected", address: "169.254.170.2:80", allowPrivate: true},
		{name: "alibaba metadata always rejected", address: "100.100.100.200:80", allowPrivate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			base := func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("stop after policy")
			}
			_, _ = guardedHTTPVersionDialContext(base, tc.allowPrivate)(context.Background(), "tcp", tc.address)
			if (calls == 1) != tc.wantDial {
				t.Fatalf("dial calls=%d", calls)
			}
		})
	}
}

func TestHTTPVersionVerifierDisablesProxyAndCustomTLSDialBypasses(t *testing.T) {
	t.Run("proxy hook is removed", func(t *testing.T) {
		proxyCalls := 0
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = func(*http.Request) (*url.URL, error) { proxyCalls++; return url.Parse("http://127.0.0.1:1") }
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: "http://127.0.0.1/version", JSONPointer: "/commit"}, Client: &http.Client{Transport: transport}}
		_, _ = v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "abc"}})
		if proxyCalls != 0 {
			t.Fatalf("proxy calls=%d", proxyCalls)
		}
	})
	t.Run("custom TLS dial hook is removed", func(t *testing.T) {
		tlsDialCalls := 0
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
			tlsDialCalls++
			return nil, errors.New("bypass")
		}
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: "https://169.254.169.254/latest/meta-data", JSONPointer: "/commit", AllowPrivate: true}, Client: &http.Client{Transport: transport}}
		_, _ = v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "abc"}})
		if tlsDialCalls != 0 {
			t.Fatalf("TLS dial calls=%d", tlsDialCalls)
		}
	})
}

func TestHTTPVersionVerifierBoundsAndSanitizesFailures(t *testing.T) {
	t.Run("https downgrade", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				t.Fatal("HTTPS redirect downgrade target was requested")
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://version.example.test/commit"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		})}
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: "https://version.example.test/start", JSONPointer: "/commit"}, Client: client}
		got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
		if err != nil || got.Result != DeploymentResultUnavailable || calls != 1 {
			t.Fatalf("got=%+v calls=%d err=%v", got, calls, err)
		}
	})
	t.Run("redirect userinfo", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if calls > 1 || request.Header.Get("Authorization") != "" {
				t.Fatal("credential-bearing redirect target was requested")
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://user:pass@version.example.test/commit"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		})}
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: "https://version.example.test/start", JSONPointer: "/commit"}, Client: client}
		got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
		if err != nil || got.Result != DeploymentResultUnavailable || calls != 1 {
			t.Fatalf("got=%+v calls=%d err=%v", got, calls, err)
		}
	})
	t.Run("cross host redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("cross-host target called") }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, target.URL, http.StatusFound)
		}))
		defer server.Close()
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit", AllowPrivate: true}}
		got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
		if err != nil || got.Result != DeploymentResultUnavailable {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("body cap", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", HTTPVersionMaxBodyBytes+1)))
		}))
		defer server.Close()
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit", AllowPrivate: true}}
		got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
		if err != nil || got.Result != DeploymentResultUnavailable || len(got.ObservedVersion) > 128 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{"commit":"a"}`))
		}))
		defer server.Close()
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit", AllowPrivate: true}, Timeout: 10 * time.Millisecond}
		got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
		if err != nil || got.Result != DeploymentResultUnavailable {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
}

func TestHTTPVersionVerifierRejectsUnsafeArrayIndexesWithoutPanic(t *testing.T) {
	for _, pointer := range []string{"/items/01", "/items/18446744073709551616", "/items/999999999999999999999999999999999999999999999999999999999"} {
		t.Run(pointer, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("pointer %q panicked: %v", pointer, recovered)
				}
			}()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"items":["a"]}`)) }))
			defer server.Close()
			v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: pointer, AllowPrivate: true}}
			got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
			if err != nil || got.Result != DeploymentResultUnavailable {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

type fakeK8sDeploymentReader struct {
	cluster, namespace, deployment string
	result                         K8sDeploymentVersion
	err                            error
}

func (f *fakeK8sDeploymentReader) ReadDeployment(_ context.Context, cluster, namespace, deployment string) (K8sDeploymentVersion, error) {
	f.cluster, f.namespace, f.deployment = cluster, namespace, deployment
	return f.result, f.err
}

func TestK8sVersionVerifierUsesConfiguredReadOnlyMapping(t *testing.T) {
	reader := &fakeK8sDeploymentReader{result: K8sDeploymentVersion{Annotations: map[string]string{"app.example.com/git-commit": "abc123"}, Images: []string{"registry/admin-web:abc123"}}}
	v := K8sVersionVerifier{Environment: "test", Config: config.K8sDeploymentVerification{Cluster: "test-cluster", Namespace: "admin-test", DeploymentsByRepo: map[string]string{"admin-web": "admin-web"}, CommitAnnotation: "app.example.com/git-commit"}, Reader: reader}
	got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "k8s", ExpectedCommits: map[string]string{"admin-web": "abc123"}})
	if err != nil || got.Result != DeploymentResultMatched || got.ObservedImages["admin-web"] != "registry/admin-web:abc123" || reader.cluster != "test-cluster" || reader.namespace != "admin-test" || reader.deployment != "admin-web" {
		t.Fatalf("got=%+v reader=%+v err=%v", got, reader, err)
	}
}

func TestK8sVersionVerifierImageLabelAndFailures(t *testing.T) {
	reader := &fakeK8sDeploymentReader{result: K8sDeploymentVersion{Labels: map[string]string{"git-commit": "abc123"}}}
	v := K8sVersionVerifier{Environment: "test", Config: config.K8sDeploymentVerification{Cluster: "c", Namespace: "n", DeploymentsByRepo: map[string]string{"repo": "deploy"}, ImageLabel: "git-commit"}, Reader: reader}
	got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "k8s", ExpectedCommits: map[string]string{"repo": "abc123"}})
	if err != nil || got.Result != DeploymentResultMatched {
		t.Fatalf("got=%+v err=%v", got, err)
	}

	reader.err = errors.New("Authorization: Bearer top-secret")
	got, err = v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "k8s", ExpectedCommits: map[string]string{"repo": "abc123"}})
	if err != nil || got.Result != DeploymentResultUnavailable || got.DiagnosticCode != "k8s_read_failed" || strings.Contains(got.ObservedVersion+got.DiagnosticMessage, "top-secret") {
		t.Fatalf("got=%+v err=%v", got, err)
	}

	got, err = v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "k8s", ExpectedCommits: map[string]string{"ghost": "abc123"}})
	if err == nil || !errors.Is(err, ErrDeploymentVerifierUnavailable) {
		t.Fatalf("got=%+v err=%v", got, err)
	}

	got, err = v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "k8s", ExpectedCommits: map[string]string{}})
	if err != nil || got.Result != DeploymentResultUnavailable {
		t.Fatalf("empty scope must fail closed: got=%+v err=%v", got, err)
	}
}
