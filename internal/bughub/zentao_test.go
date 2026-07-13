package bughub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeZentaoBug(t *testing.T) {
	raw := ZentaoBug{
		ID: "1842", Title: "支付页提交后 500", Status: "active",
		AssignedTo: "xiaolong", OpenedBy: "qa", Severity: "1", Pri: "2",
		Product: "S基建项目", Module: "frontend", Type: "codeerror", OS: "WEB", Browser: "Chrome",
		Steps: "打开支付页", Keywords: "prod,mall-web,pay-service",
	}

	got := NormalizeZentaoBug(raw)

	if got.ID != "zentao-1842" || got.Source != "zentao" || got.SourceID != "1842" {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if got.Assignee != "xiaolong" || got.Reporter != "qa" {
		t.Fatalf("people mismatch: %+v", got)
	}
	if got.Env != "prod" {
		t.Fatalf("env = %q", got.Env)
	}
	if got.Product != "S基建项目" || got.Module != "frontend" || got.BugType != "codeerror" || got.OS != "WEB" || got.Browser != "Chrome" || got.Keywords != "prod,mall-web,pay-service" {
		t.Fatalf("zentao fields mismatch: %+v", got)
	}
	if len(got.ServiceHints) == 0 || got.ServiceHints[0] != "pay-service" {
		t.Fatalf("service hints = %#v", got.ServiceHints)
	}
}

func TestNormalizeZentaoBugPrefersNamesForObjectFields(t *testing.T) {
	var raw ZentaoBug
	if err := json.Unmarshal([]byte(`{
		"id": 718,
		"title": "搜索页异常",
		"product": {"id": 3, "name": "S基建项目"},
		"module": {"id": 9, "name": "frontend"}
	}`), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := NormalizeZentaoBug(raw)

	if got.Product != "S基建项目" || got.Module != "frontend" {
		t.Fatalf("object fields = product %q module %q", got.Product, got.Module)
	}
}

func TestNormalizeZentaoBugConvertsHTMLStepsToText(t *testing.T) {
	raw := ZentaoBug{
		ID: "577", Title: "PC端搜索结果页: 电影展示为一集全",
		Steps: `<p>[步骤]</p><ol><li>PC端进入搜索页，输入电影名称并查看搜索结果。</li><li>观察电影类结果卡片展示的集数/更新信息。</li></ol><p>[结果]</p><p>搜索结果页中电影内容展示为“一集全”，与电影内容形态不匹配。</p><p>[期望]</p><p>电影类搜索结果不应展示“一集全”等剧集信息，应按电影内容规则展示正确信息。</p>`,
	}

	got := NormalizeZentaoBug(raw)

	want := "[步骤]\n- PC端进入搜索页，输入电影名称并查看搜索结果。\n- 观察电影类结果卡片展示的集数/更新信息。\n[结果]\n搜索结果页中电影内容展示为“一集全”，与电影内容形态不匹配。\n[期望]\n电影类搜索结果不应展示“一集全”等剧集信息，应按电影内容规则展示正确信息。"
	if got.Steps != want {
		t.Fatalf("steps = %q\nwant  %q", got.Steps, want)
	}
}

func TestNormalizeZentaoBugExtractsAttachments(t *testing.T) {
	raw := ZentaoBug{
		ID: "718", Title: "截图附件",
		Files: zentaoFiles{
			{ID: "101", Title: "screen.png", Extension: "png"},
			{ID: "102", Name: "trace.har", URL: "/file-read-102.html"},
		},
	}

	got := NormalizeZentaoBug(raw)

	if len(got.Attachments) != 2 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	if got.Attachments[0].ID != "101" || got.Attachments[0].Name != "screen.png" || got.Attachments[0].Type != "image/png" {
		t.Fatalf("first attachment = %+v", got.Attachments[0])
	}
	if got.Attachments[1].RemoteURL != "/file-read-102.html" {
		t.Fatalf("second attachment = %+v", got.Attachments[1])
	}
}

func TestNormalizeZentaoBugExtractsImagesFromActionComments(t *testing.T) {
	raw := ZentaoBug{
		ID: "577", Title: "评论截图",
		Actions: []zentaoAction{
			{Comment: `<p><img src="http://zentao.example.com/index.php?m=file&f=read&t=png&fileID=1129" alt="index.php?m=file&amp;f=read&amp;t=png&amp;fileID=1129" />电脑内容这个地方应该为false</p>`},
		},
	}

	got := NormalizeZentaoBug(raw)

	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	if got.Attachments[0].ID != "1129" || got.Attachments[0].Type != "image/png" {
		t.Fatalf("attachment = %+v", got.Attachments[0])
	}
	if got.Attachments[0].RemoteURL != "http://zentao.example.com/index.php?m=file&f=read&t=png&fileID=1129" {
		t.Fatalf("remote url = %q", got.Attachments[0].RemoteURL)
	}
}

func TestZentaoClientFetchAssigned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Token") != "secret" {
			t.Fatalf("Token header = %q", r.Header.Get("Token"))
		}
		if got := r.URL.Query().Get("assignedTo"); got != "" {
			t.Fatalf("assignedTo query should be filtered locally, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"},{"id":"1843","title":"别人负责的 Bug","assignedTo":"other"}]}`))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 1 || got[0].ID != "zentao-1842" {
		t.Fatalf("bugs = %+v", got)
	}
}

func TestZentaoClientFetchAssignedFallsBackToLocalFilterWhenQueryRejected(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api.php/v1/bugs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("assignedTo") != "" {
			http.Error(w, `{"error":"unsupported assignedTo"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"},{"id":"1843","title":"别人负责的 Bug","assignedTo":"other"}]}`))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if len(got) != 1 || got[0].SourceID != "1842" || got[0].Assignee != "xiaolong" {
		t.Fatalf("bugs = %+v", got)
	}
}

func TestZentaoClientFetchAssignedFallsBackToProductBugListWhenProductIDRequired(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			http.Error(w, `{"error":"Need product id."}`, http.StatusBadRequest)
		case "/api.php/v1/products":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"products":[{"id":11,"name":"PC端"},{"id":"12","name":"移动端"}]}`))
		case "/api.php/v1/products/11/bugs":
			if got := r.URL.Query().Get("browseType"); got != "all" {
				t.Fatalf("browseType = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":1842,"title":"支付页提交后 500","assignedTo":"xiaolong","severity":1,"pri":2},{"id":"1843","title":"别人负责的 Bug","assignedTo":"other"}]}`))
		case "/api.php/v1/products/12/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"1844","title":"搜索页异常","assignedTo":"xiaolong"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 2 || got[0].SourceID != "1842" || got[1].SourceID != "1844" {
		t.Fatalf("bugs = %+v", got)
	}
	if len(paths) != 4 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestZentaoClientFetchAssignedFallsBackToProductBugListWhenGlobalBugListEOF(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijack")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
		case "/api.php/v1/products":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"products":[{"id":"11","name":"PC端"}]}`))
		case "/api.php/v1/products/11/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "1842" {
		t.Fatalf("bugs = %+v", got)
	}
	if len(paths) != 3 || paths[0] != "/api.php/v1/bugs?limit=100" || paths[1] != "/api.php/v1/products?limit=100" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestZentaoClientFetchAssignedScansPaginatedProducts(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			http.Error(w, `{"error":"Need product id."}`, http.StatusBadRequest)
		case "/api.php/v1/products":
			switch r.URL.Query().Get("page") {
			case "", "1":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"page":1,"total":2,"limit":1,"products":[{"id":11,"name":"PC端"}]}`))
			case "2":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"page":2,"total":2,"limit":1,"products":[{"id":12,"name":"移动端"}]}`))
			default:
				t.Fatalf("unexpected products page %q", r.URL.Query().Get("page"))
			}
		case "/api.php/v1/products/11/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1843","title":"别人负责的 Bug","assignedTo":"other"}]}`))
		case "/api.php/v1/products/12/bugs":
			if got := r.URL.Query().Get("browseType"); got != "all" {
				t.Fatalf("browseType = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1844","title":"搜索页异常","assignedTo":"xiaolong"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "1844" {
		t.Fatalf("bugs = %+v", got)
	}
	if len(paths) != 5 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestZentaoClientFetchAssignedSupportsObjectAssignee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bugs":[{"id":1842,"title":"支付页提交后 500","assignedTo":{"account":"kevin","realname":"小龙"}},{"id":1843,"title":"别人负责的 Bug","assignedTo":{"account":"other","realname":"别人"}}]}`))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("kevin")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "1842" || got[0].Assignee != "kevin" {
		t.Fatalf("bugs = %+v", got)
	}
}

func TestZentaoClientFetchAssignedWithPasswordRequestsToken(t *testing.T) {
	var tokenRequested bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/tokens":
			tokenRequested = true
			if r.Method != http.MethodPost {
				t.Fatalf("token method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token body: %v", err)
			}
			if body["account"] != "xiaolong" || body["password"] != "pw" {
				t.Fatalf("token body = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"login-token"}`))
		case "/api.php/v1/bugs":
			if r.Header.Get("Token") != "login-token" {
				t.Fatalf("Token header = %q", r.Header.Get("Token"))
			}
			if got := r.URL.Query().Get("assignedTo"); got != "" {
				t.Fatalf("assignedTo query should be filtered locally, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Account: "xiaolong", Password: "pw", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if !tokenRequested {
		t.Fatal("token endpoint was not requested")
	}
	if len(got) != 1 || got[0].SourceID != "1842" {
		t.Fatalf("bugs = %+v", got)
	}
}

func TestZentaoClientFetchAssignedWithSessionHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "zentaosid=sso-session; lang=zh-cn" {
			t.Fatalf("Cookie header = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer feishu-sso" {
			t.Fatalf("Authorization header = %q", got)
		}
		if got := r.URL.Query().Get("assignedTo"); got != "" {
			t.Fatalf("assignedTo query should be filtered locally, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
	}))
	defer srv.Close()

	client := ZentaoClient{
		BaseURL:       srv.URL,
		Account:       "xiaolong",
		AuthMode:      "session_header",
		SessionHeader: "Cookie: zentaosid=sso-session; lang=zh-cn\nAuthorization: Bearer feishu-sso",
		HTTPClient:    srv.Client(),
	}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "1842" {
		t.Fatalf("bugs = %+v", got)
	}
}

func TestZentaoClientCurrentUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/user" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "zentaosid=sso-session" {
			t.Fatalf("Cookie header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"profile":{"account":"kevin","realname":"Kevin"}}`))
	}))
	defer srv.Close()

	client := ZentaoClient{
		BaseURL:       srv.URL,
		AuthMode:      "feishu_sso",
		SessionHeader: "Cookie: zentaosid=sso-session",
		HTTPClient:    srv.Client(),
	}
	got, err := client.CurrentUserAccount()
	if err != nil {
		t.Fatalf("CurrentUserAccount: %v", err)
	}
	if got != "kevin" {
		t.Fatalf("account = %q", got)
	}
}

func TestZentaoClientFetchByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs/1842" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}}`))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchByID("1842")
	if err != nil {
		t.Fatalf("FetchByID: %v", err)
	}
	if got.ID != "zentao-1842" || got.SourceID != "1842" || got.Title != "支付页提交后 500" {
		t.Fatalf("bug = %+v", got)
	}
}

func TestZentaoClientFetchByIDSupportsTopLevelBugResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs/577" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":577,
			"title":"评论截图",
			"actions":[{"comment":"<p><img src=\"http://zentao.example.com/index.php?m=file&f=read&t=png&fileID=1129\" /></p>"}]
		}`))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchByID("577")
	if err != nil {
		t.Fatalf("FetchByID: %v", err)
	}
	if got.SourceID != "577" || len(got.Attachments) != 1 || got.Attachments[0].ID != "1129" {
		t.Fatalf("bug = %+v attachments=%+v", got, got.Attachments)
	}
}

func TestZentaoClientFetchAttachmentUsesAuthAndFallbackURLs(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if r.Header.Get("Token") != "secret" {
			t.Fatalf("Token header = %q", r.Header.Get("Token"))
		}
		if r.URL.Path != "/file-read-101.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	data, contentType, err := client.FetchAttachment(Attachment{ID: "101", Name: "screen.png", Type: "image/png"})
	if err != nil {
		t.Fatalf("FetchAttachment: %v", err)
	}
	if contentType != "image/png" || string(data) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("attachment = %q %q", contentType, string(data))
	}
	if len(seen) != 3 || seen[0] != "/api.php/v1/files/101/download" || seen[2] != "/file-read-101.html" {
		t.Fatalf("paths = %#v", seen)
	}
}

func TestSyncZentaoAssignedStoresFetchedBugs(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			if got := r.URL.Query().Get("assignedTo"); got != "" {
				t.Fatalf("assignedTo query should be filtered locally, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"},{"id":"1843","title":"别人负责的 Bug","assignedTo":"other"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong","files":[{"id":"101","title":"screen.png","extension":"png"}]}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := NewStore(t.TempDir())
	result, err := SyncZentaoAssigned(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Token: "secret", Enabled: true,
	}, store, srv.Client())
	if err != nil {
		t.Fatalf("SyncZentaoAssigned: %v", err)
	}
	if result.Fetched != 1 || result.Stored != 1 || result.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", result)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != "zentao-1842" || items[0].Assignee != "xiaolong" {
		t.Fatalf("items = %+v", items)
	}
	if len(items[0].Attachments) != 1 || items[0].Attachments[0].Name != "screen.png" {
		t.Fatalf("attachments = %+v paths=%#v", items[0].Attachments, paths)
	}
}

func TestSyncZentaoAssignedUsesCurrentUserWhenPlatformAccountBlank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/user":
			if got := r.Header.Get("Cookie"); got != "zentaosid=sso-session" {
				t.Fatalf("user Cookie header = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"profile":{"account":"kevin","realname":"Kevin"}}`))
		case "/api.php/v1/bugs":
			if got := r.URL.Query().Get("assignedTo"); got != "" {
				t.Fatalf("assignedTo query should be filtered locally, got %q", got)
			}
			if got := r.Header.Get("Cookie"); got != "zentaosid=sso-session" {
				t.Fatalf("bugs Cookie header = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"kevin"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"kevin"}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := NewStore(t.TempDir())
	result, err := SyncZentaoAssigned(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "feishu_sso", SessionHeader: "Cookie: zentaosid=sso-session", Enabled: true,
	}, store, srv.Client())
	if err != nil {
		t.Fatalf("SyncZentaoAssigned: %v", err)
	}
	if result.Fetched != 1 || result.Stored != 1 || result.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", result)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Assignee != "kevin" {
		t.Fatalf("items = %+v", items)
	}
}

func TestSyncZentaoAssignedContinuesWhenCurrentUserLookupFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/user":
			panic(http.ErrAbortHandler)
		case "/api.php/v1/bugs":
			if got := r.Header.Get("Cookie"); got != "zentaosid=sso-session" {
				t.Fatalf("bugs Cookie header = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"kevin"},{"id":"1843","title":"搜索页异常","assignedTo":"xiaolong"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"kevin"}}`))
		case "/api.php/v1/bugs/1843":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1843","title":"搜索页异常","assignedTo":"xiaolong"}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := NewStore(t.TempDir())
	result, err := SyncZentaoAssigned(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "feishu_sso", SessionHeader: "Cookie: zentaosid=sso-session", Enabled: true,
	}, store, srv.Client())
	if err != nil {
		t.Fatalf("SyncZentaoAssigned: %v", err)
	}
	if result.Account != "" || result.Fetched != 2 || result.Stored != 2 {
		t.Fatalf("result = %+v", result)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
}

func TestSyncZentaoAssignedUsesProductBugListWhenProductIDRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/user":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"profile":{"account":"kevin","realname":"Kevin"}}`))
		case "/api.php/v1/bugs":
			http.Error(w, `{"error":"Need product id."}`, http.StatusBadRequest)
		case "/api.php/v1/products":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"products":[{"id":11,"name":"PC端"}]}`))
		case "/api.php/v1/products/11/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"kevin"},{"id":"1843","title":"别人负责的 Bug","assignedTo":"other"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"kevin"}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := NewStore(t.TempDir())
	result, err := SyncZentaoAssigned(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "feishu_sso", SessionHeader: "Cookie: zentaosid=sso-session", Enabled: true,
	}, store, srv.Client())
	if err != nil {
		t.Fatalf("SyncZentaoAssigned: %v", err)
	}
	if result.Fetched != 1 || result.Stored != 1 || result.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", result)
	}
	if result.Account != "kevin" || result.RawFetched != 2 || result.Filtered != 1 {
		t.Fatalf("sync diagnostics = %+v", result)
	}
}

func TestSyncZentaoBugStoresSingleBug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs/1842" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}}`))
	}))
	defer srv.Close()

	store := NewStore(t.TempDir())
	result, err := SyncZentaoBug(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Enabled: true,
	}, store, "1842", srv.Client())
	if err != nil {
		t.Fatalf("SyncZentaoBug: %v", err)
	}
	if result.Fetched != 1 || result.Stored != 1 || result.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", result)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].SourceID != "1842" {
		t.Fatalf("items = %+v", items)
	}
}

func TestSyncZentaoBugExtractsIDFromFeishuMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs/656" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bug":{"id":"656","title":"PC端feed流: 部分视频播放失败","assignedTo":"xiaolong"}}`))
	}))
	defer srv.Close()

	store := NewStore(t.TempDir())
	result, err := SyncZentaoBug(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Enabled: true,
	}, store, "Kevin指派了Bug #656::【codex自动化】 PC端feed流：部分视频播放失败", srv.Client())
	if err != nil {
		t.Fatalf("SyncZentaoBug: %v", err)
	}
	if result.SelectedBugID != "zentao-656" {
		t.Fatalf("result = %+v", result)
	}
}
