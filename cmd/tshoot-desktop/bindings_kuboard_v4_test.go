package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKuboardV4LoginContract(t *testing.T) {
	for _, password := range []string{"fixture-pass", "测试密码-é"} {
		t.Run(password, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/login.kuboard.cn/v4/login" || r.Method != http.MethodPost {
					t.Error("wrong login endpoint")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["username"] != "audit" || body["userSource"] != "dao" || body["password"] != base64.StdEncoding.EncodeToString([]byte(password)) {
					t.Error("incorrect login contract")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"accessToken": "header.payload.signature"}})
			}))
			defer ts.Close()
			token, err := kuboardLoginV4(context.Background(), ts.Client(), ts.URL, "audit", password)
			if err != nil || token != "header.payload.signature" {
				t.Fatal("login failed", err)
			}
		})
	}
}

func TestKuboardV4RejectedLogin(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError, http.StatusOK} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":401,"message":"bad credentials"}`))
		}))
		token, err := kuboardLoginV4(context.Background(), ts.Client(), ts.URL, "audit", "wrong")
		ts.Close()
		if err == nil || token != "" {
			t.Fatalf("rejected login accepted: HTTP %d", status)
		}
	}
}

func TestKuboardV4AuthHeaders(t *testing.T) {
	for _, tc := range []struct{ token, header, value string }{
		{"header.payload.signature", "Authorization", "Bearer header.payload.signature"},
		{"key.secret", "Kb-Access-Key", "key.secret"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		setKuboardV4Auth(req, tc.token)
		if len(req.Header) != 1 || req.Header.Get(tc.header) != tc.value {
			t.Fatal("mixed or incorrect authentication")
		}
	}
}
