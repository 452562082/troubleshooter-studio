package bughub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/git/a~1b/~0commit"}}
	got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"admin-web": "abc123"}})
	if err != nil || got.Result != DeploymentResultMatched || got.ObservedCommits["admin-web"] != "abc123" || got.VerifiedAt == nil {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
	if strings.Contains(got.ObservedVersion, "secret-response-header") {
		t.Fatal("response headers leaked")
	}
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
	t.Run("cross host redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("cross-host target called") }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Redirect(w, nil, target.URL, http.StatusFound) }))
		defer server.Close()
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit"}}
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
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit"}}
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
		v := HTTPVersionVerifier{Environment: "test", Config: config.HTTPDeploymentVerification{URL: server.URL, JSONPointer: "/commit"}, Timeout: 10 * time.Millisecond}
		got, err := v.Verify(context.Background(), DeploymentVerificationRequest{Environment: "test", Source: "http", ExpectedCommits: map[string]string{"repo": "a"}})
		if err != nil || got.Result != DeploymentResultUnavailable {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
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
	if err != nil || got.Result != DeploymentResultUnavailable || strings.Contains(got.ObservedVersion, "top-secret") {
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
