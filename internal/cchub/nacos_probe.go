// nacos_probe.go —— Nacos 的"接入握手"三件套:probe API 风格 + dashboard 兜底探测 + 登录拿 token。
// PreloadNacos / FetchContent 都依赖 connectNacos 把这套跑完后才能 list/get configs。
package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// probeFlavor 按 **v3 优先** 的顺序试路径,首个能确认是 Nacos(200 / 401 / 403 / 410)
// 的就用;v1 只作为对老版本部署(Nacos 2.x)的后备 —— Nacos 3.x 默认已禁用 v1 console API,
// 命中 v1 会返 410 提示 "请用 v3",我们会据此直接跳回 v3 + /nacos,不白试后续 candidate。
//
// 返 (flavor, human-note, err)。note 会展示到日志,方便用户确认"实际用了哪个版本"。
func (c *nacosClient) probeFlavor() (apiFlavor, string, error) {
	// v3 优先(2025+ 主流),其中 /nacos 前缀放前面(docker 默认),根路径放后面(ingress 改写场景)
	// v1 兜底(只给 Nacos 2.x / 1.x 用;3.x 命中 v1 会返 410,我们会据此切回 v3)
	candidates := []apiFlavor{
		{"/nacos", "v3"}, // Nacos 3.x 主路径
		{"", "v3"},       // Nacos 3.x 根路径部署
		{"/nacos", "v1"}, // Nacos 2.x / 1.x 后备
		{"", "v1"},       // Nacos 2.x / 1.x 根路径后备
	}
	var attempts []string
	for _, f := range candidates {
		u := c.base + f.ContextPath + "/" + f.Version + "/console/server/state"
		resp, err := c.httpCli.Get(u)
		if err != nil {
			// 网络不可达(dial timeout / DNS 解析失败),所有 candidate 都会 fail,直接返
			return apiFlavor{}, "", fmt.Errorf("连 %s 失败: %w(检查网络 / 地址 / 端口 / VPC 是否可达)", c.base, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case 200:
			note := fmt.Sprintf("检测到 Nacos:API=%s", f.Version)
			if f.ContextPath == "" {
				note += ",根路径部署(无 /nacos 前缀)"
			}
			return f, note, nil
		case 401, 403:
			// 路径对但要 auth → 仍当命中,登录流程会继续 POST 账号密码。
			// Nacos 3.x 某些部署 /v3/console/server/state 直接要登录,才能避免走到 v1 踩 410。
			note := fmt.Sprintf("检测到 Nacos:API=%s(需登录才能访问 console/state)", f.Version)
			if f.ContextPath == "" {
				note += ",根路径部署"
			}
			return f, note, nil
		case 410:
			// v1 console API 已被 3.x 禁用;响应体会提示 "please use ${contextPath:nacos}/v3/..."
			// 直接按 v3 + /nacos 走,跳过剩余 candidate 不再白试
			bodyStr := string(body)
			if strings.Contains(bodyStr, "/v3/") || strings.Contains(bodyStr, "contextPath:nacos") {
				return apiFlavor{ContextPath: "/nacos", Version: "v3"},
					"检测到 Nacos 3.x:v1 console API 已禁用,自动切换到 /nacos/v3 路径", nil
			}
			attempts = append(attempts, fmt.Sprintf("  GET %s → 410 Gone: %s", u, snippet(body)))
		default:
			attempts = append(attempts, fmt.Sprintf("  GET %s → %d %s", u, resp.StatusCode, snippet(body)))
		}
	}
	// 路径都非 200,但 base 通 → 再试一下"/"或"/nacos/"是不是 dashboard(HTML),
	// 能帮用户识别是 Nacos 但 API 路径变了 vs 地址根本不是 Nacos
	dashHint := probeDashboard(c.httpCli, c.base)
	return apiFlavor{}, "", fmt.Errorf(
		"无法定位 %s 的 Nacos API(已按 v3 优先顺序探测 4 个路径,全部未识别)。\n"+
			"尝试结果:\n%s\n%s\n可能原因:\n"+
			"  1) 地址端口不是 Nacos(确认 %s 是 Nacos 服务端,非 dashboard 反代)\n"+
			"  2) Nacos 被反向代理挡了 → 试直连端口 8848\n"+
			"  3) 私网 IP 不可达(AWS 172.31.x.x / 阿里云 VPC 等 → 开 VPN/跳板)\n"+
			"  4) Nacos 3.x 魔改 context path(非 /nacos) → 联系运维查 server.servlet.context-path",
		c.base, strings.Join(attempts, "\n"), dashHint, c.base)
}

// probeDashboard 探测一下 "/" 和 "/nacos/" 是不是返回 HTML(Nacos dashboard),
// 如果是 → 说明地址确实是 Nacos 但 API 路径变了;如果不是 → 地址根本不对。
// 这条信息加到 probeFlavor 的错误消息里,帮用户排查。
func probeDashboard(cli *http.Client, base string) string {
	for _, p := range []string{"/", "/nacos/"} {
		resp, err := cli.Get(base + p)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 && strings.Contains(strings.ToLower(string(body)), "nacos") {
			return fmt.Sprintf("探测 %s%s 返回含 \"nacos\" 的 HTML → 地址是 Nacos dashboard 但 API 路径未识别,可能被 ingress 改写过", base, p)
		}
	}
	return "探测 / 与 /nacos/ 都没看到 Nacos dashboard 特征 → 地址可能根本不是 Nacos,或 ingress 把根页面换了"
}

// login v1/v3 路径不同,form body 格式相同(x-www-form-urlencoded)。
func (c *nacosClient) login() error {
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)
	var loginPath string
	switch c.flavor.Version {
	case "v3":
		loginPath = c.flavor.ContextPath + "/v3/auth/user/login"
	default:
		loginPath = c.flavor.ContextPath + "/v1/auth/login"
	}
	resp, err := c.httpCli.PostForm(c.base+loginPath, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, snippet(body))
	}
	var doc struct {
		AccessToken string `json:"accessToken"`
		TokenTTL    int    `json:"tokenTtl"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("decode login resp: %w(body: %s)", err, snippet(body))
	}
	if doc.AccessToken == "" {
		return fmt.Errorf("accessToken 空(账号或密码错?body: %s)", snippet(body))
	}
	c.token = doc.AccessToken
	return nil
}
