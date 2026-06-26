package dsprobe

import "testing"

// TestTLSConfigForProbe 锁定探活 TLS 默认行为:
//   - 无凭据 → 跳过校验(纯连通性,无秘密可被 MITM 偷)
//   - 带凭据 → 默认校验证书(防中间人伪造证书截获 basic auth / Bearer)
//   - 带凭据 + TSHOOT_INSECURE_TLS opt-in → 恢复跳过校验(内网自签逃生口)
func TestTLSConfigForProbe(t *testing.T) {
	t.Run("无凭据_跳过校验", func(t *testing.T) {
		t.Setenv(InsecureTLSEnv, "")
		if got := TLSConfigForProbe(false); !got.InsecureSkipVerify {
			t.Errorf("无凭据应跳过校验(InsecureSkipVerify=true),got false")
		}
	})

	t.Run("带凭据_默认校验", func(t *testing.T) {
		t.Setenv(InsecureTLSEnv, "")
		got := TLSConfigForProbe(true)
		if got.InsecureSkipVerify {
			t.Errorf("带凭据默认应校验证书(InsecureSkipVerify=false),got true")
		}
		if got.MinVersion == 0 {
			t.Errorf("校验模式应设最低 TLS 版本")
		}
	})

	t.Run("带凭据_opt_in_放行", func(t *testing.T) {
		for _, v := range []string{"1", "true", "TRUE", "yes"} {
			t.Setenv(InsecureTLSEnv, v)
			if got := TLSConfigForProbe(true); !got.InsecureSkipVerify {
				t.Errorf("%s=%q 应放行跳过校验,got false", InsecureTLSEnv, v)
			}
		}
	})

	t.Run("带凭据_无效值_仍校验", func(t *testing.T) {
		t.Setenv(InsecureTLSEnv, "0")
		if got := TLSConfigForProbe(true); got.InsecureSkipVerify {
			t.Errorf("无效 opt-in 值应仍校验,got InsecureSkipVerify=true")
		}
	})
}
