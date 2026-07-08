package bughub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	root string
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Path() string {
	return filepath.Join(s.root, "bugs.json")
}

func (s *Store) List() ([]Bug, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	normalizeStoredBugs(items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s *Store) Get(id string) (Bug, bool, error) {
	items, err := s.readAll()
	if err != nil {
		return Bug{}, false, err
	}
	normalizeStoredBugs(items)
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return Bug{}, false, nil
}

func (s *Store) Upsert(b Bug) error {
	if strings.TrimSpace(b.ID) == "" {
		return errors.New("bug id is required")
	}
	if strings.TrimSpace(b.Title) == "" {
		return errors.New("bug title is required")
	}
	now := time.Now().UTC()
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = now
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	replaced := false
	for i := range items {
		if items[i].ID == b.ID {
			if b.CreatedAt.IsZero() {
				b.CreatedAt = items[i].CreatedAt
			}
			items[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		if b.CreatedAt.IsZero() {
			b.CreatedAt = now
		}
		items = append(items, b)
	}
	return s.writeAll(items)
}

// PruneStale 删除指定来源+平台的 bug 中不在 keepIDs 集合里的条目。
// 用于同步后清理禅道上已修复/关闭/重新指派的 bug。
// platformID 为空时退化为只按 source 匹配（兼容老数据没有 platform_id 的情况）。
func (s *Store) PruneStale(source, platformID string, keepIDs []string) (int, error) {
	items, err := s.readAll()
	if err != nil {
		return 0, err
	}
	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	pruned := 0
	filtered := make([]Bug, 0, len(items))
	for _, item := range items {
		if item.Source == source {
			if platformID == "" || item.PlatformID == "" || item.PlatformID == platformID {
				if !keep[item.ID] {
					pruned++
					continue
				}
			}
		}
		filtered = append(filtered, item)
	}
	if pruned == 0 {
		return 0, nil
	}
	if err := s.writeAll(filtered); err != nil {
		return 0, err
	}
	return pruned, nil
}

func (s *Store) readAll() ([]Bug, error) {
	data, err := os.ReadFile(s.Path())
	if os.IsNotExist(err) {
		return []Bug{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []Bug{}, nil
	}
	var items []Bug
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return []Bug{}, nil
	}
	return items, nil
}

func normalizeStoredBugs(items []Bug) {
	for i := range items {
		if items[i].Source == "zentao" {
			items[i].Steps = zentaoHTMLToText(items[i].Steps)
			items[i].Description = zentaoHTMLToText(items[i].Description)
			items[i].Expected = zentaoHTMLToText(items[i].Expected)
			items[i].Actual = zentaoHTMLToText(items[i].Actual)
		}
	}
}

func (s *Store) writeAll(items []Bug) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(), append(data, '\n'), 0o600)
}
