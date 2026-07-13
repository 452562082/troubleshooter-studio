package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	tshoot "github.com/xiaolong/troubleshooter-studio"
	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/doctor"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// Server 封装 Web API 的依赖
type Server struct {
	TemplateRoot string
}

// HandleValidate POST /api/validate — body 为 troubleshooter.yaml 内容
func (s *Server) HandleValidate(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFromBody(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"valid":  true,
		"system": cfg.System.ID,
		"name":   cfg.System.Name,
		"envs":   len(cfg.Environments),
		"repos":  len(cfg.Repos),
		"issues": config.HealthCheck(cfg),
	})
}

// HandlePrefillCreds POST /api/prefill-creds — body 为 troubleshooter.yaml 内容,返回 env var key → value
// (KUBOARD_URL_DEV 这种 install 阶段环境变量名)。空值字段不返。
func (s *Server) HandlePrefillCreds(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFromBody(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, agent.PrefillCredsFromYAML(cfg))
}

// HandlePlan POST /api/plan — body 为 troubleshooter.yaml 内容
func (s *Server) HandlePlan(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFromBody(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	outDir, err := os.MkdirTemp("", "tshoot-plan-*")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, s.TemplateRoot, outDir)
	plan, err := g.BuildPlan("")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, plan)
}

// HandleGen POST /api/gen — body 为 troubleshooter.yaml 内容，返回 GenSummary
//
// outDir 走 MkdirTemp + defer RemoveAll —— 跟 HandlePlan 行为对齐。
// 旧版固定写到 "./dist" 在多用户 / 并发请求场景下会互相覆盖产物 + Summary 失真,
// 单用户也累积残留撑大磁盘。HandleGen 只返 Summary(stats),产物本身用户自己用
// /api/gen 之前应已 fork 拷贝出去,server 侧 Generate 完即可清干净。
func (s *Server) HandleGen(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFromBody(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	outDir, err := os.MkdirTemp("", "tshoot-gen-*")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, s.TemplateRoot, outDir)
	if err := g.Generate(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, g.Summary)
}

// HandleDoctor POST /api/doctor — body 为 troubleshooter.yaml 内容 + query ?repos_root=
func (s *Server) HandleDoctor(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFromBody(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	reposRoot := r.URL.Query().Get("repos_root")
	rep, err := doctor.Check(cfg, reposRoot)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, rep)
}

// HandleSchema GET /api/schema — 返回 troubleshooter.schema.yaml 内容
func (s *Server) HandleSchema(w http.ResponseWriter, r *http.Request) {
	schemaPath := filepath.Join(filepath.Dir(s.TemplateRoot), "schema", "troubleshooter.schema.yaml")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		data, err = tshoot.SchemaFS.ReadFile("schema/troubleshooter.schema.yaml")
		if err != nil {
			jsonError(w, http.StatusNotFound, "schema file not found: "+err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Write(data)
}

// --- helpers ---

func loadConfigFromBody(r *http.Request) (*config.SystemConfig, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()
	return config.LoadFromBytes(data)
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
