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
		"unsupported version":   strings.Replace(string(valid), "version: 1", "version: 2", 1),
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
