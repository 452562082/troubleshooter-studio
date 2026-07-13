package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func (s *Server) HandleBugHook(w http.ResponseWriter, r *http.Request) {
	platformID := strings.TrimPrefix(r.URL.Path, "/api/bug-hooks/")
	platformID = strings.Trim(platformID, "/")
	if platformID == "" {
		jsonError(w, http.StatusNotFound, "platform id is required")
		return
	}

	platforms := bughub.NewPlatformStore(bughub.DefaultRoot())
	platform, ok, err := platforms.Get(platformID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		jsonError(w, http.StatusNotFound, "bug platform not found")
		return
	}
	if !validBugHookSecret(r, platform.HookSecret) {
		jsonError(w, http.StatusUnauthorized, "invalid hook secret")
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()
	bug, result, err := bughub.BugFromWebhook(platform, data)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !result.Accepted {
		jsonOK(w, result)
		return
	}
	if err := bughub.NewStore(bughub.DefaultRoot()).Upsert(bug); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.StoredID = bug.ID
	jsonOK(w, result)
}

func validBugHookSecret(r *http.Request, want string) bool {
	if strings.TrimSpace(want) == "" {
		return false
	}
	for _, got := range []string{
		r.URL.Query().Get("secret"),
		r.Header.Get("X-Tshoot-Bug-Secret"),
		r.Header.Get("X-Hook-Secret"),
	} {
		if subtleStringEqual(got, want) {
			return true
		}
	}
	return false
}

func subtleStringEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	var out byte
	for i := 0; i < len(a); i++ {
		out |= a[i] ^ b[i]
	}
	return out == 0
}

func decodeJSONBody(r *http.Request, out any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("empty body")
	}
	return json.Unmarshal(data, out)
}
