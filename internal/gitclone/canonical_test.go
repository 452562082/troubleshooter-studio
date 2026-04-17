package gitclone

import "testing"

func TestCanonicalURL(t *testing.T) {
	cases := map[string]string{
		// scp-style ssh
		"git@github.com:shop/order-service.git": "github.com/shop/order-service",
		"git@github.com:shop/order-service":     "github.com/shop/order-service",
		// https
		"https://github.com/shop/order-service.git": "github.com/shop/order-service",
		"https://github.com/shop/order-service/":    "github.com/shop/order-service",
		"http://gitlab.com/a/b.git":                 "gitlab.com/a/b",
		// ssh://
		"ssh://git@github.com/shop/order-service.git": "github.com/shop/order-service",
		"ssh://github.com/shop/order-service":         "github.com/shop/order-service",
		// 大小写
		"GIT@GITHUB.COM:Shop/Order-Service.GIT": "github.com/shop/order-service",
		// 空串 / 无效
		"": "",
	}
	for in, want := range cases {
		got := CanonicalURL(in)
		if got != want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalURL_CrossProtocolEqual(t *testing.T) {
	urls := []string{
		"git@github.com:shop/order-service.git",
		"https://github.com/shop/order-service",
		"ssh://git@github.com/shop/order-service.git",
	}
	first := CanonicalURL(urls[0])
	for _, u := range urls[1:] {
		if got := CanonicalURL(u); got != first {
			t.Errorf("cross-protocol should canonicalize equal: %q → %q vs %q", u, got, first)
		}
	}
}
