package bughub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
)

type zenString string

func (s *zenString) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = zenString(stringFromAny(raw))
	return nil
}

func stringFromAny(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case bool:
		return fmt.Sprint(v)
	case map[string]any:
		return firstNonEmpty(
			stringFromAny(v["account"]),
			stringFromAny(v["id"]),
			stringFromAny(v["realname"]),
			stringFromAny(v["name"]),
		)
	case []any:
		for _, item := range v {
			if s := stringFromAny(item); s != "" {
				return s
			}
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func displayStringFromAny(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case bool:
		return fmt.Sprint(v)
	case map[string]any:
		return firstNonEmpty(
			displayStringFromAny(v["name"]),
			displayStringFromAny(v["title"]),
			displayStringFromAny(v["realname"]),
			displayStringFromAny(v["account"]),
			displayStringFromAny(v["id"]),
		)
	case []any:
		for _, item := range v {
			if s := displayStringFromAny(item); s != "" {
				return s
			}
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (s zenString) String() string {
	return string(s)
}

type zenDisplayString string

func (s *zenDisplayString) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = zenDisplayString(displayStringFromAny(raw))
	return nil
}

func (s zenDisplayString) String() string {
	return string(s)
}

type ZentaoBug struct {
	ID           zenString        `json:"id"`
	Title        zenString        `json:"title"`
	Status       zenString        `json:"status"`
	AssignedTo   zenString        `json:"assignedTo"`
	OpenedBy     zenString        `json:"openedBy"`
	Severity     zenString        `json:"severity"`
	Pri          zenString        `json:"pri"`
	Product      zenDisplayString `json:"product"`
	Module       zenDisplayString `json:"module"`
	Type         zenString        `json:"type"`
	OS           zenString        `json:"os"`
	Steps        zenString        `json:"steps"`
	Keywords     zenString        `json:"keywords"`
	Browser      zenString        `json:"browser"`
	ResolvedBy   zenString        `json:"resolvedBy"`
	OpenedDate   zenString        `json:"openedDate"`
	EditedDate   zenString        `json:"editedDate"`
	ResolvedDate zenString        `json:"resolvedDate"`
	Files        zentaoFiles      `json:"files"`
	Attachments  zentaoFiles      `json:"attachments"`
	Actions      []zentaoAction   `json:"actions"`
}

type zentaoFile struct {
	ID          zenString `json:"id"`
	Title       zenString `json:"title"`
	Name        zenString `json:"name"`
	Filename    zenString `json:"filename"`
	Pathname    zenString `json:"pathname"`
	Extension   zenString `json:"extension"`
	Type        zenString `json:"type"`
	Size        zenString `json:"size"`
	URL         zenString `json:"url"`
	WebPath     zenString `json:"webPath"`
	DownloadURL zenString `json:"downloadUrl"`
}

type zentaoAction struct {
	Comment zenString `json:"comment"`
}

type zentaoFiles []zentaoFile

func (f *zentaoFiles) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) || bytes.Equal(data, []byte(`""`)) {
		*f = nil
		return nil
	}
	var list []zentaoFile
	if err := json.Unmarshal(data, &list); err == nil {
		*f = list
		return nil
	}
	var mapped map[string]zentaoFile
	if err := json.Unmarshal(data, &mapped); err == nil {
		list = make([]zentaoFile, 0, len(mapped))
		for key, item := range mapped {
			if item.ID.String() == "" {
				item.ID = zenString(key)
			}
			list = append(list, item)
		}
		*f = list
		return nil
	}
	return nil
}

type ZentaoProduct struct {
	ID   string
	Name string
}

type zentaoPageInfo struct {
	Page  int
	Total int
	Limit int
}

type ZentaoAssignedResult struct {
	Bugs         []Bug
	RawFetched   int
	Filtered     int
	ProductCount int
}

type ZentaoClient struct {
	BaseURL       string
	Account       string
	AuthMode      string
	SessionHeader string
	Password      string
	Token         string
	HTTPClient    *http.Client
}

type ZentaoHTTPError struct {
	Prefix     string
	Status     string
	StatusCode int
	Body       string
}

func (e *ZentaoHTTPError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("%s %s", e.Prefix, e.Status)
	}
	return fmt.Sprintf("%s %s: %s", e.Prefix, e.Status, e.Body)
}

func IsZentaoUnauthorized(err error) bool {
	var statusErr *ZentaoHTTPError
	return errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusUnauthorized
}

func NormalizeZentaoBug(raw ZentaoBug) Bug {
	id := raw.ID.String()
	env, frontend, hints := parseZentaoKeywords(raw.Keywords.String())
	return Bug{
		ID:           "zentao-" + id,
		Source:       "zentao",
		SourceID:     id,
		Title:        raw.Title.String(),
		Status:       raw.Status.String(),
		Severity:     raw.Severity.String(),
		Priority:     raw.Pri.String(),
		Product:      raw.Product.String(),
		Module:       raw.Module.String(),
		BugType:      raw.Type.String(),
		OS:           raw.OS.String(),
		Browser:      raw.Browser.String(),
		Keywords:     raw.Keywords.String(),
		Assignee:     raw.AssignedTo.String(),
		Reporter:     raw.OpenedBy.String(),
		Steps:        zentaoHTMLToText(raw.Steps.String()),
		Env:          env,
		FrontendRepo: frontend,
		ServiceHints: hints,
		Attachments:  normalizeZentaoAttachments(raw.Files, raw.Attachments, zentaoActionCommentFiles(raw.Actions), zentaoImageFilesFromHTML(raw.Steps.String())),
		CreatedAt:    parseZentaoTime(raw.OpenedDate.String()),
		UpdatedAt:    firstTime(parseZentaoTime(raw.EditedDate.String()), parseZentaoTime(raw.ResolvedDate.String()), time.Now().UTC()),
		RawPreview:   raw.Title.String(),
	}
}

func normalizeZentaoAttachments(groups ...zentaoFiles) []Attachment {
	out := make([]Attachment, 0)
	seen := map[string]bool{}
	for _, files := range groups {
		for _, file := range files {
			id := strings.TrimSpace(file.ID.String())
			name := firstNonEmpty(file.Title.String(), file.Name.String(), file.Filename.String(), file.Pathname.String())
			if name == "" && id == "" {
				continue
			}
			remote := firstNonEmpty(file.DownloadURL.String(), file.URL.String(), file.WebPath.String())
			key := firstNonEmpty(id, remote, name)
			if seen[key] {
				continue
			}
			seen[key] = true
			if name == "" {
				name = "附件 " + id
			}
			out = append(out, Attachment{
				ID:        id,
				Name:      name,
				Type:      firstNonEmpty(file.Type.String(), attachmentTypeFromName(name, file.Extension.String())),
				RemoteURL: remote,
			})
		}
	}
	return out
}

func zentaoActionCommentFiles(actions []zentaoAction) zentaoFiles {
	var out zentaoFiles
	for _, action := range actions {
		out = append(out, zentaoImageFilesFromHTML(action.Comment.String())...)
	}
	return out
}

func zentaoImageFilesFromHTML(input string) zentaoFiles {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	var out zentaoFiles
	tokenizer := nethtml.NewTokenizer(strings.NewReader(input))
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case nethtml.ErrorToken:
			return out
		case nethtml.SelfClosingTagToken, nethtml.StartTagToken:
			name, hasAttr := tokenizer.TagName()
			if strings.ToLower(string(name)) != "img" {
				continue
			}
			var src, alt string
			for hasAttr {
				key, val, more := tokenizer.TagAttr()
				switch strings.ToLower(string(key)) {
				case "src":
					src = strings.TrimSpace(stdhtml.UnescapeString(string(val)))
				case "alt":
					alt = strings.TrimSpace(stdhtml.UnescapeString(string(val)))
				}
				hasAttr = more
			}
			if src == "" {
				continue
			}
			if strings.HasPrefix(src, "{") {
				continue
			}
			id, ext := zentaoFileIDAndExtFromURL(src)
			title := firstNonEmpty(zentaoImageNameFromAlt(alt), zentaoImageNameFromURL(src), "评论图片 "+id)
			out = append(out, zentaoFile{
				ID:        zenString(id),
				Title:     zenString(title),
				Extension: zenString(ext),
				Type:      zenString(attachmentTypeFromName(title, ext)),
				URL:       zenString(src),
			})
		}
	}
}

func zentaoFileIDAndExtFromURL(rawURL string) (string, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	q := u.Query()
	return strings.TrimSpace(q.Get("fileID")), strings.TrimSpace(q.Get("t"))
}

func zentaoImageNameFromAlt(alt string) string {
	alt = strings.TrimSpace(alt)
	if alt == "" || strings.Contains(alt, "index.php?") {
		return ""
	}
	return alt
}

func zentaoImageNameFromURL(rawURL string) string {
	id, ext := zentaoFileIDAndExtFromURL(rawURL)
	if id == "" {
		return ""
	}
	if ext == "" {
		return "评论图片 " + id
	}
	return "评论图片 " + id + "." + strings.TrimPrefix(ext, ".")
}

func attachmentTypeFromName(name string, ext string) string {
	ext = strings.Trim(strings.ToLower(strings.TrimSpace(ext)), ".")
	if ext == "" {
		if idx := strings.LastIndex(name, "."); idx >= 0 && idx < len(name)-1 {
			ext = strings.ToLower(name[idx+1:])
		}
	}
	switch ext {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	default:
		return ext
	}
}

func zentaoHTMLToText(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if !strings.Contains(input, "<") || !strings.Contains(input, ">") {
		return stdhtml.UnescapeString(input)
	}
	var b strings.Builder
	tokenizer := nethtml.NewTokenizer(strings.NewReader(input))
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case nethtml.ErrorToken:
			return cleanZentaoPlainText(b.String())
		case nethtml.TextToken:
			appendZentaoText(&b, stdhtml.UnescapeString(string(tokenizer.Text())))
		case nethtml.StartTagToken:
			name, _ := tokenizer.TagName()
			switch strings.ToLower(string(name)) {
			case "br", "p", "div", "section", "tr", "table", "ul", "ol":
				appendZentaoNewline(&b)
			case "li":
				appendZentaoNewline(&b)
				b.WriteString("- ")
			}
		case nethtml.EndTagToken:
			name, _ := tokenizer.TagName()
			switch strings.ToLower(string(name)) {
			case "p", "div", "section", "li", "tr", "table", "ul", "ol":
				appendZentaoNewline(&b)
			}
		}
	}
}

func appendZentaoText(b *strings.Builder, text string) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return
	}
	current := b.String()
	if current != "" && !strings.HasSuffix(current, "\n") && !strings.HasSuffix(current, " ") && !strings.HasSuffix(current, "- ") {
		b.WriteByte(' ')
	}
	b.WriteString(text)
}

func appendZentaoNewline(b *strings.Builder) {
	current := b.String()
	if current == "" || strings.HasSuffix(current, "\n") {
		return
	}
	b.WriteByte('\n')
}

func cleanZentaoPlainText(input string) string {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (c ZentaoClient) FetchAssigned(account string) ([]Bug, error) {
	result, err := c.FetchAssignedWithStats(account)
	if err != nil {
		return nil, err
	}
	return result.Bugs, nil
}

func (c ZentaoClient) FetchAssignedWithStats(account string) (ZentaoAssignedResult, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return ZentaoAssignedResult{}, fmt.Errorf("zentao base url is required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/api.php/v1/bugs")
	if err != nil {
		return ZentaoAssignedResult{}, err
	}
	q := u.Query()
	q.Set("limit", "100")
	u.RawQuery = q.Encode()
	raw, err := c.fetchBugList(u.String())
	productCount := 0
	if err != nil {
		if !shouldFallbackToZentaoProductBugList(err) {
			return ZentaoAssignedResult{}, err
		}
		raw, productCount, err = c.fetchProductBugList()
		if err != nil {
			return ZentaoAssignedResult{}, err
		}
	}
	bugs := normalizeAndFilterZentaoBugs(raw, account)
	return ZentaoAssignedResult{
		Bugs:         bugs,
		RawFetched:   len(raw),
		Filtered:     len(bugs),
		ProductCount: productCount,
	}, nil
}

func (c ZentaoClient) fetchBugList(rawURL string) ([]ZentaoBug, error) {
	var out []ZentaoBug
	nextURL := rawURL
	for page := 1; ; page++ {
		raw, info, err := c.fetchBugListPage(nextURL)
		if err != nil {
			return nil, err
		}
		out = append(out, raw...)
		if !shouldFetchNextZentaoPage(info, len(out), len(raw), page) {
			return out, nil
		}
		nextURL = zentaoPageURL(rawURL, page+1)
	}
}

func (c ZentaoClient) fetchBugListPage(rawURL string) ([]ZentaoBug, zentaoPageInfo, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, zentaoPageInfo{}, err
	}
	if err := c.applyAuth(req, client); err != nil {
		return nil, zentaoPageInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, zentaoPageInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zentaoPageInfo{}, zentaoStatusError(resp, "zentao returned")
	}
	var payload struct {
		Bugs  []ZentaoBug `json:"bugs"`
		Data  []ZentaoBug `json:"data"`
		Page  int         `json:"page"`
		Total int         `json:"total"`
		Limit int         `json:"limit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, zentaoPageInfo{}, err
	}
	raw := payload.Bugs
	if len(raw) == 0 {
		raw = payload.Data
	}
	return raw, zentaoPageInfo{Page: payload.Page, Total: payload.Total, Limit: payload.Limit}, nil
}

func normalizeAndFilterZentaoBugs(raw []ZentaoBug, account string) []Bug {
	out := make([]Bug, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		id := item.ID.String()
		if strings.TrimSpace(id) == "" || strings.TrimSpace(item.Title.String()) == "" {
			continue
		}
		if seen[id] {
			continue
		}
		bug := NormalizeZentaoBug(item)
		if account != "" && !strings.EqualFold(strings.TrimSpace(bug.Assignee), strings.TrimSpace(account)) {
			continue
		}
		seen[id] = true
		out = append(out, bug)
	}
	return out
}

func (c ZentaoClient) fetchProductBugList() ([]ZentaoBug, int, error) {
	products, err := c.FetchProducts()
	if err != nil {
		return nil, 0, err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	var out []ZentaoBug
	for _, product := range products {
		if strings.TrimSpace(product.ID) == "" {
			continue
		}
		u, err := url.Parse(base + "/api.php/v1/products/" + url.PathEscape(product.ID) + "/bugs")
		if err != nil {
			return nil, len(products), err
		}
		q := u.Query()
		q.Set("browseType", "all")
		q.Set("limit", "100")
		u.RawQuery = q.Encode()
		bugs, err := c.fetchBugList(u.String())
		if err != nil {
			return nil, len(products), fmt.Errorf("fetch zentao product %s bugs: %w", product.ID, err)
		}
		out = append(out, bugs...)
	}
	return out, len(products), nil
}

func (c ZentaoClient) FetchProducts() ([]ZentaoProduct, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("zentao base url is required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/api.php/v1/products")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("limit", "100")
	u.RawQuery = q.Encode()
	var out []ZentaoProduct
	nextURL := u.String()
	for page := 1; ; page++ {
		raw, info, err := c.fetchProductPage(nextURL)
		if err != nil {
			return nil, err
		}
		for _, item := range raw {
			id := strings.TrimSpace(stringFromAny(item["id"]))
			if id == "" {
				continue
			}
			out = append(out, ZentaoProduct{
				ID:   id,
				Name: strings.TrimSpace(stringFromAny(item["name"])),
			})
		}
		if !shouldFetchNextZentaoPage(info, len(out), len(raw), page) {
			return out, nil
		}
		nextURL = zentaoPageURL(u.String(), page+1)
	}
}

func (c ZentaoClient) fetchProductPage(rawURL string) ([]map[string]any, zentaoPageInfo, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, zentaoPageInfo{}, err
	}
	if err := c.applyAuth(req, client); err != nil {
		return nil, zentaoPageInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, zentaoPageInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zentaoPageInfo{}, zentaoStatusError(resp, "zentao products returned")
	}
	var payload struct {
		Products []map[string]any `json:"products"`
		Data     []map[string]any `json:"data"`
		Page     int              `json:"page"`
		Total    int              `json:"total"`
		Limit    int              `json:"limit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, zentaoPageInfo{}, err
	}
	raw := payload.Products
	if len(raw) == 0 {
		raw = payload.Data
	}
	return raw, zentaoPageInfo{Page: payload.Page, Total: payload.Total, Limit: payload.Limit}, nil
}

func shouldFetchNextZentaoPage(info zentaoPageInfo, fetched int, lastPageItems int, requestedPage int) bool {
	if info.Total <= 0 || info.Limit <= 0 || lastPageItems == 0 {
		return false
	}
	if fetched >= info.Total {
		return false
	}
	currentPage := info.Page
	if currentPage <= 0 {
		currentPage = requestedPage
	}
	return currentPage*info.Limit < info.Total
}

func zentaoPageURL(rawURL string, page int) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("page", fmt.Sprint(page))
	u.RawQuery = q.Encode()
	return u.String()
}

func (c ZentaoClient) CurrentUserAccount() (string, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("zentao base url is required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/api.php/v1/user")
	if err != nil {
		return "", err
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if err := c.applyAuth(req, client); err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", zentaoStatusError(resp, "zentao current user returned")
	}
	var payload struct {
		Profile struct {
			Account string `json:"account"`
		} `json:"profile"`
		User struct {
			Account string `json:"account"`
		} `json:"user"`
		Data struct {
			Account string `json:"account"`
			Profile struct {
				Account string `json:"account"`
			} `json:"profile"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	account := firstNonEmpty(payload.Profile.Account, payload.User.Account, payload.Data.Account, payload.Data.Profile.Account)
	account = strings.TrimSpace(account)
	if account == "" {
		return "", fmt.Errorf("zentao current user response did not include account")
	}
	return account, nil
}

func (c ZentaoClient) FetchByID(id string) (Bug, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Bug{}, fmt.Errorf("zentao bug id is required")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return Bug{}, fmt.Errorf("zentao base url is required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/api.php/v1/bugs/" + url.PathEscape(id))
	if err != nil {
		return Bug{}, err
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return Bug{}, err
	}
	if err := c.applyAuth(req, client); err != nil {
		return Bug{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Bug{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Bug{}, zentaoStatusError(resp, "zentao returned")
	}
	var root json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return Bug{}, err
	}
	var payload struct {
		Bug  ZentaoBug `json:"bug"`
		Data ZentaoBug `json:"data"`
	}
	_ = json.Unmarshal(root, &payload)
	raw := payload.Bug
	if strings.TrimSpace(raw.ID.String()) == "" {
		raw = payload.Data
	}
	if strings.TrimSpace(raw.ID.String()) == "" {
		_ = json.Unmarshal(root, &raw)
	}
	if strings.TrimSpace(raw.ID.String()) == "" || strings.TrimSpace(raw.Title.String()) == "" {
		return Bug{}, fmt.Errorf("zentao bug %s not found in response", id)
	}
	return NormalizeZentaoBug(raw), nil
}

func (c ZentaoClient) HydrateBugDetails(bugs []Bug) []Bug {
	out := make([]Bug, 0, len(bugs))
	for _, bug := range bugs {
		if strings.TrimSpace(bug.SourceID) == "" {
			out = append(out, bug)
			continue
		}
		detail, err := c.FetchByID(bug.SourceID)
		if err != nil {
			out = append(out, bug)
			continue
		}
		out = append(out, mergeZentaoBugDetail(bug, detail))
	}
	return out
}

func mergeZentaoBugDetail(listBug Bug, detail Bug) Bug {
	if detail.ID == "" {
		return listBug
	}
	if detail.Env == "" {
		detail.Env = listBug.Env
	}
	if detail.BotEnv == "" {
		detail.BotEnv = listBug.BotEnv
	}
	if detail.SystemID == "" {
		detail.SystemID = listBug.SystemID
	}
	if detail.FrontendRepo == "" {
		detail.FrontendRepo = listBug.FrontendRepo
	}
	if len(detail.ServiceHints) == 0 {
		detail.ServiceHints = listBug.ServiceHints
	}
	if detail.FrontendURL == "" {
		detail.FrontendURL = listBug.FrontendURL
	}
	if len(detail.APIPaths) == 0 {
		detail.APIPaths = listBug.APIPaths
	}
	if len(detail.TraceIDs) == 0 {
		detail.TraceIDs = listBug.TraceIDs
	}
	if len(detail.RequestIDs) == 0 {
		detail.RequestIDs = listBug.RequestIDs
	}
	// 合并两边的附件：detail API 可能不返回评论区图片，不能直接覆盖 list 的
	detail.Attachments = mergeAttachments(detail.Attachments, listBug.Attachments)
	if detail.SelectedBotKey == "" {
		detail.SelectedBotKey = listBug.SelectedBotKey
	}
	if detail.LastContext == "" {
		detail.LastContext = listBug.LastContext
		detail.LastContextAt = listBug.LastContextAt
	}
	if detail.RawPreview == "" {
		detail.RawPreview = listBug.RawPreview
	}
	return detail
}

// mergeAttachments 合并两个附件列表：detail 优先，list 里不在 detail 中的补进来。
// detail API 可能不返回评论区内的图片，直接覆盖会导致 list API 提取的评论图片丢失。
func mergeAttachments(detail, list []Attachment) []Attachment {
	seen := make(map[string]bool, len(detail))
	out := make([]Attachment, 0, len(detail)+len(list))
	add := func(a Attachment) bool {
		key := strings.TrimSpace(a.ID)
		if key == "" {
			key = strings.TrimSpace(a.RemoteURL)
		}
		if key == "" {
			key = strings.TrimSpace(a.Name)
		}
		if key == "" || seen[key] {
			return false
		}
		seen[key] = true
		out = append(out, a)
		return true
	}
	for _, a := range detail {
		add(a)
	}
	for _, a := range list {
		add(a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c ZentaoClient) FetchAttachment(att Attachment) ([]byte, string, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, "", fmt.Errorf("zentao base url is required")
	}
	urls := zentaoAttachmentURLs(c.BaseURL, att)
	if len(urls) == 0 {
		return nil, "", fmt.Errorf("attachment %s has no downloadable url", att.Name)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	var lastErr error
	for _, rawURL := range urls {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if err := c.applyAuth(req, client); err != nil {
			return nil, "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = &ZentaoHTTPError{
				Prefix:     "zentao attachment returned",
				Status:     resp.Status,
				StatusCode: resp.StatusCode,
				Body:       strings.TrimSpace(string(data)),
			}
			continue
		}
		contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		return data, contentType, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("attachment %s could not be downloaded", att.Name)
	}
	return nil, "", lastErr
}

func zentaoAttachmentURLs(baseURL string, att Attachment) []string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if u, err := url.Parse(raw); err == nil && u.IsAbs() {
			out = append(out, raw)
			return
		}
		if strings.HasPrefix(raw, "/") {
			out = append(out, base+raw)
			return
		}
		out = append(out, base+"/"+raw)
	}
	add(att.RemoteURL)
	if id := strings.TrimSpace(att.ID); id != "" {
		escaped := url.PathEscape(id)
		add("/api.php/v1/files/" + escaped + "/download")
		add("/api.php/v1/files/" + escaped)
		add("/file-read-" + escaped + ".html")
		add("/file-download-" + escaped + ".html")
	}
	return cleanStrings(out)
}

func (c ZentaoClient) applyAuth(req *http.Request, client *http.Client) error {
	switch strings.TrimSpace(strings.ToLower(c.AuthMode)) {
	case "", "session_header", "feishu_sso":
		if strings.TrimSpace(c.SessionHeader) != "" {
			return applyHeaderLines(req.Header, c.SessionHeader)
		}
	case "api_token", "token":
		token := strings.TrimSpace(c.Token)
		if token != "" {
			req.Header.Set("Token", token)
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	case "password":
		// handled below by authToken, exchanging account/password for token
	}
	token, err := c.authToken(client)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Token", token)
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

func applyHeaderLines(header http.Header, raw string) error {
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("invalid header line %q, want 'Name: value'", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return fmt.Errorf("invalid header line %q, want 'Name: value'", line)
		}
		header.Set(key, value)
	}
	return nil
}

func (c ZentaoClient) authToken(client *http.Client) (string, error) {
	if strings.TrimSpace(c.Token) != "" {
		return strings.TrimSpace(c.Token), nil
	}
	if strings.TrimSpace(c.Account) == "" || strings.TrimSpace(c.Password) == "" {
		return "", nil
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("zentao base url is required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/api.php/v1/tokens")
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{
		"account":  strings.TrimSpace(c.Account),
		"password": c.Password,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", zentaoStatusError(resp, "zentao token returned")
	}
	var payload struct {
		Token string `json:"token"`
		Data  struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	token := strings.TrimSpace(payload.Token)
	if token == "" {
		token = strings.TrimSpace(payload.Data.Token)
	}
	if token == "" {
		return "", fmt.Errorf("zentao token response did not include token")
	}
	return token, nil
}

func zentaoStatusError(resp *http.Response, prefix string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return &ZentaoHTTPError{
		Prefix:     prefix,
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}
}

func isZentaoProductIDRequired(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "need product id") || strings.Contains(msg, "product id")
}

func shouldFallbackToZentaoProductBugList(err error) bool {
	if err == nil {
		return false
	}
	if isZentaoProductIDRequired(err) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "server closed")
}

func parseZentaoKeywords(keywords string) (env string, frontend string, hints []string) {
	for _, part := range strings.Split(keywords, ",") {
		part = strings.TrimSpace(part)
		lower := strings.ToLower(part)
		switch {
		case lower == "prod" || lower == "production" || lower == "test" || lower == "dev" || lower == "staging" || lower == "pre":
			env = lower
		case strings.HasSuffix(lower, "-web") || strings.HasSuffix(lower, "-admin") || strings.Contains(lower, "frontend"):
			frontend = part
		case part != "":
			hints = append(hints, part)
		}
	}
	return env, frontend, cleanStrings(hints)
}

func parseZentaoTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0000-00-00") {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstTime(items ...time.Time) time.Time {
	for _, item := range items {
		if !item.IsZero() {
			return item
		}
	}
	return time.Time{}
}
