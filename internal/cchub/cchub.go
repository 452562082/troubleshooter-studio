package cchub

import "fmt"

// Preload 按 req.Type 分派到具体 client(nacos / apollo / consul)。
// 未识别的 type 返 error,不兜 —— UI 应在请求前就校验过 type。
func Preload(req Request) (*Result, error) {
	switch req.Type {
	case "nacos":
		return PreloadNacos(req)
	case "apollo":
		return PreloadApollo(req)
	case "consul":
		return PreloadConsul(req)
	}
	return nil, fmt.Errorf("unsupported config center type: %q", req.Type)
}
