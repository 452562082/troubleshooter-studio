package bughub

import (
	"fmt"
	"strings"
	"testing"
)

func browserPlanWithAction(actionYAML string) []byte {
	return []byte(fmt.Sprintf(`version: 1
start_url: https://test.example.com/users
actions:
%s
assertions:
  - kind: visible_text
    value: 汤圆
`, actionYAML))
}

func TestParseBrowserPlanAcceptsExactActionMatrix(t *testing.T) {
	cases := map[string]string{
		"goto": `  - id: go-users
    action: goto
    url: users
    screenshot_after: true`,
		"click": `  - id: open-users
    action: click
    locator: {kind: role, value: tab, name: 用户}
    screenshot_after: true`,
		"click_test_id": `  - id: open-search
    action: click
    locator: {kind: test_id, value: user-search}`,
		"fill": `  - id: enter-name
    action: fill
    locator: {kind: placeholder, value: 请输入用户昵称}
    value: 汤圆`,
		"press": `  - id: submit-search
    action: press
    locator: {kind: css, value: "#search"}
    key: Enter`,
		"select": `  - id: select-role
    action: select
    locator: {kind: label, value: 角色}
    value: admin`,
		"upload_file": `  - id: upload-sheet
    action: upload_file
    locator: {kind: css, value: "input[type=file]"}
    file_ref: file-123`,
		"wait_for": `  - id: wait-results
    action: wait_for
    locator: {kind: text, value: 搜索结果}
    screenshot_after: true`,
		"screenshot": `  - id: capture-results
    action: screenshot
    screenshot_after: false`,
	}
	for name, actionYAML := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := ParseBrowserPlan(browserPlanWithAction(actionYAML))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Version != 1 || len(plan.Actions) != 1 || plan.Actions[0].ID == "" {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}

func TestParseBrowserPlanWaitForHiddenStateAndTimeout(t *testing.T) {
	const raw = `version: 2
device_profile: desktop
scenario_contract:
  version: 1
  goal: wait for asynchronous content to settle
  basis: bug
  causal_action_ids: [capture-final]
  evidence:
    - kind: ui_assertions
start_url: https://app.example.com
actions:
  - id: wait-loading
    action: wait_for
    locator: {kind: text, value: 加载中...}
    state: hidden
    timeout_ms: 20000
  - id: capture-final
    action: screenshot
assertions:
  - kind: visible_text
    value: 用户头像
`
	plan, err := ParseBrowserPlan([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Actions[0]; got.State != "hidden" || got.TimeoutMS != 20_000 {
		t.Fatalf("wait action=%+v", got)
	}

	for name, replacement := range map[string]string{
		"invalid state":    "state: detached\n    timeout_ms: 20000",
		"excess timeout":   "state: hidden\n    timeout_ms: 60001",
		"non-integer time": "state: hidden\n    timeout_ms: \"20000\"",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := strings.Replace(raw, "state: hidden\n    timeout_ms: 20000", replacement, 1)
			if _, err := ParseBrowserPlan([]byte(candidate)); err == nil {
				t.Fatal("expected invalid wait fields")
			}
		})
	}
}

func TestParseBrowserPlanV2AcceptsGlobalEscapeWithoutLocator(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
device_profile: desktop
scenario_contract:
  version: 1
  goal: 关闭当前活动弹窗后继续验证
  basis: bug
  causal_action_ids: [dismiss-dialog]
  evidence:
    - kind: ui_assertions
start_url: https://test.example.com/users
actions:
  - id: dismiss-dialog
    action: press
    key: Escape
assertions:
  - kind: visible_text
    value: 用户管理
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Locator != nil || plan.Actions[0].Key != "Escape" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestParseBrowserPlanRejectsUnsafeGlobalPressWithoutLocator(t *testing.T) {
	tests := []string{
		`version: 1
start_url: https://test.example.com/users
actions:
  - id: dismiss-dialog
    action: press
    key: Escape
assertions:
  - kind: visible_text
    value: 用户管理
`,
		`version: 2
device_profile: desktop
scenario_contract:
  version: 1
  goal: 提交搜索
  basis: bug
  causal_action_ids: [submit-search]
  evidence:
    - kind: ui_assertions
start_url: https://test.example.com/users
actions:
  - id: submit-search
    action: press
    key: Enter
assertions:
  - kind: visible_text
    value: 用户管理
`,
	}
	for _, raw := range tests {
		if _, err := ParseBrowserPlan([]byte(raw)); err == nil || !strings.Contains(err.Error(), "locator") {
			t.Fatalf("expected locator validation failure, got %v", err)
		}
	}
}

func TestParseBrowserPlanAcceptsPositiveAndNegativeTextAssertions(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 1
start_url: https://test.example.com/users
actions:
  - id: capture-results
    action: screenshot
assertions:
  - kind: visible_text
    value: 推荐
  - kind: not_visible_text
    value: "2022"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Assertions) != 2 || plan.Assertions[1].Kind != "not_visible_text" {
		t.Fatalf("plan assertions = %+v", plan.Assertions)
	}
}

func TestParseBrowserPlanV2PreservesExactLocatorAndObservationAssertion(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://test.example.com/users
actions:
  - id: open-search
    action: click
    locator: {kind: text, value: 搜索, exact: true}
assertions:
  - kind: page_loaded
    value: document
`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != BrowserPlanVersion || plan.Actions[0].Locator == nil || plan.Actions[0].Locator.Exact == nil || !*plan.Actions[0].Locator.Exact {
		t.Fatalf("plan = %+v", plan)
	}
	if err := validateDurableBrowserPlan(plan); err != nil {
		t.Fatalf("v2 plan is not durable: %v", err)
	}
}

func TestParseBrowserPlanCanonicalizesExplicitEmptyOptionalCollections(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
device_profile: desktop
scenario_contract:
  version: 1
  goal: 验证页面状态
  basis: bug
  frontend_entry_ids: []
  causal_action_ids: [capture]
  evidence:
    - kind: ui_assertions
start_url: https://test.example.com/users
actions:
  - id: capture
    action: screenshot
assertions:
  - kind: visible_text
    value: 用户
request_captures: []
response_assertions: []
`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.RequestCaptures != nil || plan.ResponseAssertions != nil || plan.ScenarioContract == nil || plan.ScenarioContract.FrontendEntryIDs != nil {
		t.Fatalf("optional collections were not canonicalized: %+v", plan)
	}
	if err := validateDurableBrowserPlan(plan); err != nil {
		t.Fatalf("explicit empty optional collections must remain durable: %v", err)
	}
}

func TestParseBrowserPlanValidatesScenarioContractReferences(t *testing.T) {
	valid := `version: 2
device_profile: desktop
scenario_contract:
  version: 1
  goal: 缺少必填字段时请求应被拒绝
  basis: latest_user_clarification
  causal_action_ids: [upload]
  evidence:
    - kind: response_assertion
      assertion_id: reject-upload
start_url: https://app.example.com
actions:
  - id: upload
    action: upload_file
    locator: {kind: css, value: 'input[type="file"]'}
    file_ref: case-file
assertions: []
response_assertions:
  - id: reject-upload
    action_id: upload
    kind: http_status_rejected
`
	plan, err := ParseBrowserPlan([]byte(valid))
	if err != nil || plan.ScenarioContract == nil || plan.ScenarioContract.CausalActionIDs[0] != "upload" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}

	tests := []string{
		strings.Replace(valid, "causal_action_ids: [upload]", "causal_action_ids: [missing]", 1),
		strings.Replace(valid, "assertion_id: reject-upload", "assertion_id: missing", 1),
		strings.Replace(valid, "evidence:\n    - kind: response_assertion\n      assertion_id: reject-upload", "evidence:\n    - kind: ui_assertions", 1),
	}
	for _, raw := range tests {
		if _, err := ParseBrowserPlan([]byte(raw)); err == nil {
			t.Fatalf("invalid scenario contract was accepted:\n%s", raw)
		}
	}
}

func TestBrowserCredentialSemanticDoesNotConfuseAuthorWithAuthentication(t *testing.T) {
	for _, safe := range []string{"fill-author-nickname", "author", "author_name", "authority-list"} {
		if browserStrongCredentialSemantic(safe) {
			t.Fatalf("ordinary business field %q was classified as credential material", safe)
		}
	}
	for _, unsafe := range []string{"auth", "authentication", "authorization", "auth-token", "password"} {
		if !browserStrongCredentialSemantic(unsafe) {
			t.Fatalf("credential field %q was not classified", unsafe)
		}
	}
}

func TestParseBrowserPlanV2AcceptsNamedRoleScopeForRepeatedControls(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
device_profile: desktop
start_url: https://app.example.com/videos
actions:
  - id: view-target-video
    action: click
    locator:
      kind: role
      value: link
      name: 查看
      exact: true
      within:
        kind: role
        value: row
        name: 测试都市生活剧
        exact: false
assertions:
  - kind: visible_text
    value: 视频详情
`))
	if err != nil {
		t.Fatal(err)
	}
	locator := plan.Actions[0].Locator
	if locator == nil || locator.Within == nil || locator.Within.Value != "row" || locator.Within.Name != "测试都市生活剧" {
		t.Fatalf("scoped locator=%+v", locator)
	}
}

func TestParseBrowserPlanRejectsLegacyOrNestedLocatorScope(t *testing.T) {
	legacy := `version: 1
start_url: https://app.example.com/videos
actions:
  - id: view-target-video
    action: click
    locator:
      kind: role
      value: link
      name: 查看
      within: {kind: role, value: row, name: 测试都市生活剧}
assertions:
  - kind: visible_text
    value: 视频详情
`
	if _, err := ParseBrowserPlan([]byte(legacy)); err == nil || !strings.Contains(err.Error(), "requires a non-nested version 2 locator") {
		t.Fatalf("legacy scoped locator error=%v", err)
	}
	nested := strings.Replace(legacy, "version: 1", "version: 2\ndevice_profile: desktop", 1)
	nested = strings.Replace(nested, "within: {kind: role, value: row, name: 测试都市生活剧}", "within: {kind: role, value: row, name: 测试都市生活剧, within: {kind: role, value: region, name: 内容}}", 1)
	if _, err := ParseBrowserPlan([]byte(nested)); err == nil || !strings.Contains(err.Error(), "requires a non-nested version 2 locator") {
		t.Fatalf("nested scoped locator error=%v", err)
	}
}

func TestParseBrowserPlanV2AcceptsMobileResponseFieldAssertion(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
device_profile: mobile
start_url: https://test.example.com/search
actions:
  - id: submit-search
    action: press
    locator: {kind: placeholder, value: 请输入搜索关键字, exact: true}
    key: Enter
    screenshot_after: true
assertions: []
request_captures:
  - id: search-request
    action_id: submit-search
    url_contains: /search
    source: query
    fields: [target_user_id]
response_assertions:
  - id: nickname-and-signature-differ
    action_id: submit-search
    url_contains: /search
    kind: json_fields_not_equal
    left_field: nick_name
    right_field: text
`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeviceProfile != "mobile" || len(plan.Assertions) != 0 || len(plan.ResponseAssertions) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	assertion := plan.ResponseAssertions[0]
	if assertion.ActionID != "submit-search" || assertion.Kind != "json_fields_not_equal" || assertion.LeftField != "nick_name" || assertion.RightField != "text" {
		t.Fatalf("response assertion = %+v", assertion)
	}
}

func TestParseBrowserPlanV2AcceptsHTTPStatusRejectedWithoutRequestCapture(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
device_profile: desktop
start_url: https://test.example.com/import
actions:
  - id: upload-invalid-file
    action: upload_file
    locator: {kind: css, value: 'input[type="file"][accept*=".xlsx"]'}
    file_ref: invalid-media-sheet
assertions: []
response_assertions:
  - id: import-must-reject-invalid-row
    action_id: upload-invalid-file
    url_contains: /admin/common/excel/import
    method: POST
    kind: http_status_rejected
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RequestCaptures) != 0 || len(plan.ResponseAssertions) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	assertion := plan.ResponseAssertions[0]
	if assertion.Method != "POST" || assertion.Kind != "http_status_rejected" || assertion.LeftField != "" || assertion.RightField != "" {
		t.Fatalf("response assertion = %+v", assertion)
	}
}

func TestParseBrowserPlanV2RejectsFieldsOnHTTPStatusAssertion(t *testing.T) {
	_, err := ParseBrowserPlan([]byte(`version: 2
device_profile: desktop
start_url: https://test.example.com/import
actions:
  - id: upload-invalid-file
    action: upload_file
    locator: {kind: css, value: 'input[type="file"][accept*=".xlsx"]'}
    file_ref: invalid-media-sheet
assertions: []
response_assertions:
  - id: import-must-reject-invalid-row
    action_id: upload-invalid-file
    method: POST
    kind: http_status_rejected
    left_field: code
    right_field: message
`))
	if err == nil || !strings.Contains(err.Error(), "forbids JSON field paths") {
		t.Fatalf("status assertion fields error=%v", err)
	}
}

func TestParseBrowserPlanV2AcceptsBoundedRequestCaptures(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
device_profile: desktop
start_url: https://test.example.com/search
actions:
  - id: submit-search
    action: press
    locator: {kind: placeholder, value: 请输入搜索关键字, exact: true}
    key: Enter
assertions:
  - kind: visible_text
    value: 搜索结果
request_captures:
  - id: search-parameters
    action_id: submit-search
    url_contains: /api/search
    method: POST
    source: json
    fields: [target_user_id, filters.category]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RequestCaptures) != 1 || plan.RequestCaptures[0].Fields[1] != "filters.category" {
		t.Fatalf("request captures=%+v", plan.RequestCaptures)
	}
}

func TestParseBrowserPlanV2RejectsSensitiveRequestCaptureFields(t *testing.T) {
	for _, field := range []string{"password", "access_token", "headers.authorization", "session-id"} {
		plan := fmt.Sprintf(`version: 2
start_url: https://test.example.com/search
actions:
  - id: submit-search
    action: press
    locator: {kind: placeholder, value: 搜索, exact: true}
    key: Enter
assertions:
  - kind: visible_text
    value: 搜索结果
request_captures:
  - id: unsafe
    action_id: submit-search
    source: json
    fields: [%s]
`, field)
		if _, err := ParseBrowserPlan([]byte(plan)); err == nil {
			t.Fatalf("accepted sensitive request field %q", field)
		}
	}
}

func TestParseBrowserPlanV2RejectsRequestCaptureOnNonRequestAction(t *testing.T) {
	plan := `version: 2
start_url: https://test.example.com/search
actions:
  - id: snapshot
    action: screenshot
assertions:
  - kind: visible_text
    value: 搜索结果
request_captures:
  - id: impossible
    action_id: snapshot
    source: query
    fields: [target_user_id]
`
	if _, err := ParseBrowserPlan([]byte(plan)); err == nil || !strings.Contains(err.Error(), "request-capable action") {
		t.Fatalf("non-request capture error=%v", err)
	}
}

func TestParseBrowserPlanV2RejectsUnsafeResponseAssertions(t *testing.T) {
	base := `version: 2
device_profile: desktop
start_url: https://test.example.com/search
actions:
  - id: submit-search
    action: press
    locator: {kind: placeholder, value: 搜索, exact: true}
    key: Enter
assertions: []
request_captures:
  - id: search-request
    action_id: submit-search
    source: query
    fields: [target_user_id]
response_assertions:
  - id: compare-fields
    action_id: %s
    kind: %s
    left_field: %s
    right_field: text
`
	tests := []struct {
		name, actionID, kind, left string
	}{
		{name: "unknown action", actionID: "missing", kind: "json_fields_not_equal", left: "nick_name"},
		{name: "unknown kind", actionID: "submit-search", kind: "contains_secret", left: "nick_name"},
		{name: "unsafe field", actionID: "submit-search", kind: "json_fields_not_equal", left: "users[0].nick_name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseBrowserPlan([]byte(fmt.Sprintf(base, test.actionID, test.kind, test.left))); err == nil {
				t.Fatal("expected response assertion validation error")
			}
		})
	}
}

func TestParseBrowserPlanLegacyPlanAcceptsAdditiveExactLocator(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 1
start_url: https://test.example.com/users
actions:
  - id: open-search
    action: click
    locator: {kind: text, value: 搜索, exact: true}
assertions:
  - kind: visible_text
    value: 搜索
`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0].Locator.Exact == nil || !*plan.Actions[0].Locator.Exact {
		t.Fatalf("legacy exact locator was lost: %+v", plan.Actions[0].Locator)
	}
}

func TestParseBrowserPlanRejectsMeaninglessExactLocator(t *testing.T) {
	for _, locator := range []string{
		`{kind: test_id, value: search, exact: true}`,
		`{kind: css, value: "#search", exact: true}`,
		`{kind: role, value: button, exact: true}`,
	} {
		raw := fmt.Sprintf(`version: 2
start_url: https://test.example.com/users
actions:
  - id: open-search
    action: click
    locator: %s
assertions:
  - kind: visible_text
    value: 搜索
`, locator)
		if _, err := ParseBrowserPlan([]byte(raw)); err == nil {
			t.Fatalf("expected exact locator %s to be rejected", locator)
		}
	}
}

func TestParseBrowserPlanRejectsInvalidActionFields(t *testing.T) {
	cases := map[string]string{
		"goto missing url": `  - id: step
    action: goto`,
		"goto locator forbidden": `  - id: step
    action: goto
    url: users
    locator: {kind: text, value: 用户}`,
		"click missing locator": `  - id: step
    action: click`,
		"click value forbidden": `  - id: step
    action: click
    locator: {kind: text, value: 用户}
    value: unexpected`,
		"fill missing value": `  - id: step
    action: fill
    locator: {kind: label, value: 用户}`,
		"fill key forbidden": `  - id: step
    action: fill
    locator: {kind: label, value: 用户}
    value: 汤圆
    key: Enter`,
		"press missing key": `  - id: step
    action: press
    locator: {kind: text, value: 搜索}`,
		"press value forbidden": `  - id: step
    action: press
    locator: {kind: text, value: 搜索}
    key: Enter
    value: unexpected`,
		"select missing value": `  - id: step
    action: select
    locator: {kind: label, value: 角色}`,
		"select url forbidden": `  - id: step
    action: select
    locator: {kind: label, value: 角色}
    value: admin
    url: users`,
		"upload missing file_ref": `  - id: step
    action: upload_file
    locator: {kind: css, value: "input[type=file]"}`,
		"upload value forbidden": `  - id: step
    action: upload_file
    locator: {kind: css, value: "input[type=file]"}
    file_ref: file-123
    value: /tmp/arbitrary.xlsx`,
		"click file_ref forbidden": `  - id: step
    action: click
    locator: {kind: text, value: 用户}
    file_ref: file-123`,
		"wait_for missing locator": `  - id: step
    action: wait_for`,
		"wait_for key forbidden": `  - id: step
    action: wait_for
    locator: {kind: text, value: 结果}
    key: Enter`,
		"screenshot locator forbidden": `  - id: step
    action: screenshot
    locator: {kind: text, value: 结果}`,
		"screenshot_after forbidden for screenshot": `  - id: step
    action: screenshot
    screenshot_after: true`,
	}
	for name, actionYAML := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserPlan(browserPlanWithAction(actionYAML)); err == nil {
				t.Fatal("expected action field validation error")
			}
		})
	}
}

func TestParseBrowserPlanRejectsExplicitForbiddenFieldPresence(t *testing.T) {
	cases := map[string]string{
		"non-role empty name": `  - id: step
    action: click
    locator: {kind: text, value: 用户, name: ""}`,
		"non-role null name": `  - id: step
    action: click
    locator: {kind: text, value: 用户, name: null}`,
		"click null url": `  - id: step
    action: click
    locator: {kind: text, value: 用户}
    url: null`,
		"screenshot null locator": `  - id: step
    action: screenshot
    locator: null`,
		"required goto url null": `  - id: step
    action: goto
    url: null`,
		"required click locator null": `  - id: step
    action: click
    locator: null`,
	}
	for name, actionYAML := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserPlan(browserPlanWithAction(actionYAML)); err == nil {
				t.Fatal("expected explicit null or forbidden field presence to be rejected")
			}
		})
	}
}

func TestParseBrowserPlanStrictlyValidatesStructureAndAllowlists(t *testing.T) {
	valid := browserPlanWithAction(`  - id: open-users
    action: click
    locator: {kind: role, value: tab, name: 用户}
    screenshot_after: true`)
	cases := map[string]string{
		"unknown field":         strings.Replace(string(valid), "version: 1", "version: 1\nevaluate: alert(1)", 1),
		"unknown action field":  strings.Replace(string(valid), "screenshot_after: true", "screenshot_after: true\n    timeout: 1", 1),
		"unknown locator field": strings.Replace(string(valid), "name: 用户}", "name: 用户, xpath: //button}", 1),
		"unknown action":        strings.Replace(string(valid), "action: click", "action: evaluate", 1),
		"xpath":                 strings.Replace(string(valid), "kind: role", "kind: xpath", 1),
		"duplicate id":          strings.Replace(string(valid), "assertions:", "  - id: open-users\n    action: screenshot\nassertions:", 1),
		"unsupported version":   strings.Replace(string(valid), "version: 1", "version: 3", 1),
		"empty start_url":       strings.Replace(string(valid), "start_url: https://test.example.com/users", "start_url: ''", 1),
		"empty action id":       strings.Replace(string(valid), "id: open-users", "id: ''", 1),
		"non-role name":         strings.Replace(string(valid), "kind: role, value: tab, name: 用户", "kind: text, value: tab, name: 用户", 1),
		"unknown assertion":     strings.Replace(string(valid), "kind: visible_text", "kind: title", 1),
		"empty assertion value": strings.Replace(string(valid), "value: 汤圆\n", "value: ''\n", 1),
		"multiple documents":    string(valid) + "---\nversion: 1\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserPlan([]byte(raw)); err == nil {
				t.Fatal("expected strict validation error")
			}
		})
	}
}

func TestParseBrowserPlanEnforcesBounds(t *testing.T) {
	tooMany := strings.Builder{}
	for i := 0; i < 41; i++ {
		fmt.Fprintf(&tooMany, "  - id: shot-%d\n    action: screenshot\n", i)
	}
	cases := map[string][]byte{
		"no actions": []byte(`version: 1
start_url: x
actions: []
assertions: [{kind: visible_text, value: ok}]
`),
		"more than forty actions": browserPlanWithAction(tooMany.String()),
		"string over 4096 bytes": browserPlanWithAction(fmt.Sprintf(`  - id: step
    action: click
    locator: {kind: text, value: %q}`, strings.Repeat("界", 1366))),
		"no assertions": []byte(`version: 1
start_url: x
actions: [{id: shot, action: screenshot}]
assertions: []
`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserPlan(raw); err == nil {
				t.Fatal("expected plan bound validation error")
			}
		})
	}
}

func TestParseBrowserPlanDefersURLSemanticsToHostPolicy(t *testing.T) {
	raw := browserPlanWithAction(`  - id: go-relative
    action: goto
    url: relative/path`)
	raw = []byte(strings.Replace(string(raw), "https://test.example.com/users", "relative/start", 1))
	if _, err := ParseBrowserPlan(raw); err != nil {
		t.Fatalf("syntax parser must leave URL policy to the host: %v", err)
	}
}

func TestParseBrowserPlanAcceptsHostOwnedDismissSurfaceOnlyInV2(t *testing.T) {
	raw := []byte(`version: 2
start_url: https://app.test/content
actions:
  - id: close-author-selector
    action: dismiss_surface
assertions:
  - kind: visible_text
    value: 用户信息管理
`)
	plan, err := ParseBrowserPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Action != "dismiss_surface" || plan.Actions[0].Locator != nil || plan.Actions[0].Key != "" {
		t.Fatalf("plan=%+v", plan)
	}

	for name, invalid := range map[string]string{
		"legacy":           strings.Replace(string(raw), "version: 2", "version: 1", 1),
		"locator":          strings.Replace(string(raw), "    action: dismiss_surface", "    action: dismiss_surface\n    locator: {kind: text, value: 关闭, exact: true}", 1),
		"key":              strings.Replace(string(raw), "    action: dismiss_surface", "    action: dismiss_surface\n    key: Escape", 1),
		"screenshot_after": strings.Replace(string(raw), "    action: dismiss_surface", "    action: dismiss_surface\n    screenshot_after: false", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserPlan([]byte(invalid)); err == nil {
				t.Fatal("invalid dismiss_surface action was accepted")
			}
		})
	}
}

func TestParseBrowserAssistanceRequestAcceptsBoundedBusinessQuestions(t *testing.T) {
	request, err := ParseBrowserAssistanceRequest([]byte(`assistance_status: needs_user_input
questions:
  - id: confirm_auto_upload
    question: " 选择文件后页面是否会自动上传？ "
    answer_hint: " 请说明是否还需要再次点击提交。 "
  - id: confirm_success_state
    question: 成功后应在哪个端看到什么结果？
    answer_hint: 说明页面状态或接口结果即可。
`))
	if err != nil {
		t.Fatal(err)
	}
	if request.AssistanceStatus != "needs_user_input" || len(request.Questions) != 2 ||
		request.Questions[0].Question != "选择文件后页面是否会自动上传？" ||
		request.Questions[0].AnswerHint != "请说明是否还需要再次点击提交。" {
		t.Fatalf("request=%+v", request)
	}
}

func TestParseBrowserAssistanceRequestRejectsUnsafeOrInvalidOutput(t *testing.T) {
	cases := map[string]string{
		"unknown field": `assistance_status: needs_user_input
reason: guess
questions:
  - {id: flow, question: 实际流程是什么？, answer_hint: 描述关键操作。}`,
		"wrong status": `assistance_status: blocked
questions:
  - {id: flow, question: 实际流程是什么？, answer_hint: 描述关键操作。}`,
		"no questions": `assistance_status: needs_user_input
questions: []`,
		"duplicate id": `assistance_status: needs_user_input
questions:
  - {id: flow, question: 实际流程是什么？, answer_hint: 描述关键操作。}
  - {id: flow, question: 成功结果是什么？, answer_hint: 描述页面结果。}`,
		"bad id": `assistance_status: needs_user_input
questions:
  - {id: Flow.Question, question: 实际流程是什么？, answer_hint: 描述关键操作。}`,
		"sensitive": `assistance_status: needs_user_input
questions:
  - {id: auth, question: "请提供 password: secret", answer_hint: 粘贴账号密码。}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserAssistanceRequest([]byte(raw)); err == nil {
				t.Fatal("expected strict assistance request validation error")
			}
		})
	}
}
