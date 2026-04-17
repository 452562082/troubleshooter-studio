package analyzer

import "fmt"

type Registry struct {
	byStack      map[string]Analyzer
	configCenter string
}

// NewRegistry 构造注册表；configCenter 决定每个 stack 的 analyzer 使用哪个 scanner
func NewRegistry(configCenter string) *Registry {
	r := &Registry{byStack: map[string]Analyzer{}, configCenter: configCenter}
	r.Register(NewGoAnalyzer(configCenter))
	r.Register(NewJavaAnalyzer(configCenter))
	r.Register(NewNodeAnalyzerWithCC(configCenter))
	r.Register(NewPHPAnalyzer(configCenter))
	r.Register(NewPythonAnalyzer(configCenter))
	return r
}

func (r *Registry) Register(a Analyzer) {
	r.byStack[a.Stack()] = a
}

func (r *Registry) Get(stack string) (Analyzer, error) {
	a, ok := r.byStack[stack]
	if !ok {
		return nil, fmt.Errorf("no analyzer for stack %q", stack)
	}
	return a, nil
}

func (r *Registry) ConfigCenter() string { return r.configCenter }
