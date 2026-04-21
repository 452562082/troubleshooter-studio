package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/doctor"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// Server 封装 Web API 的依赖
type Server struct {
	TemplateRoot string
}

// HandleValidate POST /api/validate — body 为 system.yaml 内容
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
	})
}

// HandlePlan POST /api/plan — body 为 system.yaml 内容
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

// HandleGen POST /api/gen — body 为 system.yaml 内容，返回 GenSummary
func (s *Server) HandleGen(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFromBody(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	outDir := cfg.Generation.OutputDir
	if outDir == "" {
		outDir = "./dist"
	}
	if !filepath.IsAbs(outDir) {
		outDir, _ = filepath.Abs(outDir)
	}

	g := generator.New(cfg, s.TemplateRoot, outDir)
	if err := g.Generate(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, g.Summary)
}

// HandleDoctor POST /api/doctor — body 为 system.yaml 内容 + query ?repos_root=
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

// HandleSchema GET /api/schema — 返回 system.schema.yaml 内容
func (s *Server) HandleSchema(w http.ResponseWriter, r *http.Request) {
	schemaPath := filepath.Join(filepath.Dir(s.TemplateRoot), "schema", "system.schema.yaml")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		jsonError(w, http.StatusNotFound, "schema file not found: "+err.Error())
		return
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

	// 写到临时文件再 Load（复用已有的校验逻辑）
	tmp, err := os.CreateTemp("", "tshoot-api-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	return config.Load(tmp.Name())
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
