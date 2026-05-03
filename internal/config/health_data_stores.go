package config

// checkDataStoresMessaging:data_stores / messaging 全 disabled 时给 info 提示。
func checkDataStoresMessaging(c *SystemConfig) []HealthIssue {
	var out []HealthIssue

	if len(c.Infrastructure.DataStores) > 0 {
		anyEnabled := false
		for _, ds := range c.Infrastructure.DataStores {
			if ds.Enabled {
				anyEnabled = true
				break
			}
		}
		if !anyEnabled {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "data_stores",
				Field:    "infrastructure.data_stores",
				Message:  "data_stores 都 disabled,所有 *-runtime-query skill 会被跳过",
			})
		}
	}

	if len(c.Infrastructure.Messaging) > 0 {
		anyEnabled := false
		for _, m := range c.Infrastructure.Messaging {
			if m.Enabled {
				anyEnabled = true
				break
			}
		}
		if !anyEnabled {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "messaging",
				Field:    "infrastructure.messaging",
				Message:  "messaging 都 disabled,机器人不会主动通知",
			})
		}
	}
	return out
}
