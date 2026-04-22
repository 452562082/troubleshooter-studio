package generator

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// FileDiff 单文件变化
type FileDiff struct {
	RelPath string `json:"rel_path"`
	Kind    string `json:"kind"` // added / removed / modified / same
}

// ConfigMapRowDiff config-map.yaml 的单行（env, service）变化
type ConfigMapRowDiff struct {
	Env       string `json:"env"`
	Service   string `json:"service"`
	OldStatus string `json:"old_status,omitempty"`
	NewStatus string `json:"new_status,omitempty"`
	Kind      string `json:"kind"` // added / removed / status-change / fields-change
	Detail    string `json:"detail,omitempty"`
}

type DiffReport struct {
	Files            []FileDiff         `json:"files"`
	ConfigMapChanges []ConfigMapRowDiff `json:"config_map_changes"`
}

func Diff(oldDir, newDir string) (*DiffReport, error) {
	rep := &DiffReport{}
	oldFiles, err := listFiles(oldDir)
	if err != nil {
		return nil, err
	}
	newFiles, err := listFiles(newDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for rel := range oldFiles {
		seen[rel] = true
	}
	for rel := range newFiles {
		seen[rel] = true
	}
	rels := make([]string, 0, len(seen))
	for r := range seen {
		rels = append(rels, r)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		oldH, okOld := oldFiles[rel]
		newH, okNew := newFiles[rel]
		switch {
		case okOld && !okNew:
			rep.Files = append(rep.Files, FileDiff{RelPath: rel, Kind: "removed"})
		case !okOld && okNew:
			rep.Files = append(rep.Files, FileDiff{RelPath: rel, Kind: "added"})
		case oldH != newH:
			rep.Files = append(rep.Files, FileDiff{RelPath: rel, Kind: "modified"})
		}
	}

	oldCM := filepath.Join(oldDir, "templates", "workspace-template", "skills", "routing", "references", "config-map.yaml")
	newCM := filepath.Join(newDir, "templates", "workspace-template", "skills", "routing", "references", "config-map.yaml")
	if rows, err := diffConfigMap(oldCM, newCM); err == nil {
		rep.ConfigMapChanges = rows
	}
	return rep, nil
}

func listFiles(root string) (map[string]string, error) {
	out := map[string]string{}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return out, nil //nolint:nilerr // 读不到忽略继续
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		out[rel] = fmt.Sprintf("%x", h[:])
		return nil
	})
	return out, err
}

func diffConfigMap(oldPath, newPath string) ([]ConfigMapRowDiff, error) {
	oldRows, _ := loadConfigMapRows(oldPath)
	newRows, _ := loadConfigMapRows(newPath)
	var diffs []ConfigMapRowDiff
	seen := map[string]bool{}
	for k := range oldRows {
		seen[k] = true
	}
	for k := range newRows {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o, oOK := oldRows[k]
		n, nOK := newRows[k]
		env, svc := splitKey(k)
		switch {
		case oOK && !nOK:
			diffs = append(diffs, ConfigMapRowDiff{Env: env, Service: svc, OldStatus: o["status"], Kind: "removed"})
		case !oOK && nOK:
			diffs = append(diffs, ConfigMapRowDiff{Env: env, Service: svc, NewStatus: n["status"], Kind: "added"})
		default:
			if o["status"] != n["status"] {
				diffs = append(diffs, ConfigMapRowDiff{Env: env, Service: svc, OldStatus: o["status"], NewStatus: n["status"], Kind: "status-change"})
			} else if !equalFields(o, n) {
				diffs = append(diffs, ConfigMapRowDiff{Env: env, Service: svc, OldStatus: o["status"], NewStatus: n["status"], Kind: "fields-change", Detail: fieldsDiff(o, n)})
			}
		}
	}
	return diffs, nil
}

func loadConfigMapRows(path string) (map[string]map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root struct {
		Environments map[string]map[string]map[string]yaml.Node `yaml:"environments"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for env, services := range root.Environments {
		for svc, fields := range services {
			row := map[string]string{}
			for k, v := range fields {
				var s string
				if v.Decode(&s) == nil {
					row[k] = s
				} else {
					var arr []string
					if v.Decode(&arr) == nil {
						row[k] = fmt.Sprintf("%v", arr)
					}
				}
			}
			out[env+"|"+svc] = row
		}
	}
	return out, nil
}

func splitKey(k string) (env, svc string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '|' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func equalFields(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func fieldsDiff(a, b map[string]string) string {
	var keys []string
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for _, k := range keys {
		if a[k] != b[k] {
			if s != "" {
				s += ", "
			}
			s += fmt.Sprintf("%s: %q → %q", k, a[k], b[k])
		}
	}
	return s
}
