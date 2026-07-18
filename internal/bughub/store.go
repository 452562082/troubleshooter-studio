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

const (
	BugInboxActive  = "active"
	BugInboxHistory = "history"

	BugArchiveNoLongerAssigned = "no_longer_assigned"
	BugArchiveSourceResolved   = "source_resolved"
)

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

func (s *Store) ListInbox() ([]Bug, error) {
	return s.listByInboxState(BugInboxActive)
}

func (s *Store) ListHistory() ([]Bug, error) {
	return s.listByInboxState(BugInboxHistory)
}

func (s *Store) listByInboxState(state string) ([]Bug, error) {
	items, err := s.List()
	if err != nil {
		return nil, err
	}
	filtered := make([]Bug, 0, len(items))
	for _, item := range items {
		if item.InboxState == state {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
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
	if bugSourceResolutionComplete(b.Status) {
		b.InboxState = BugInboxHistory
		if b.ArchivedAt == nil {
			b.ArchivedAt = &now
		}
		if strings.TrimSpace(b.ArchiveReason) == "" {
			b.ArchiveReason = BugArchiveSourceResolved
		}
	} else if b.InboxState != BugInboxHistory {
		// An assigned/open Bug returned by a later sync is active again. Clearing
		// the archive fields makes reopen/reassignment reversible. Local edits
		// loaded from history retain their explicit history marker.
		b.InboxState = BugInboxActive
		b.ArchivedAt = nil
		b.ArchiveReason = ""
	}
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

func (s *Store) Archive(id, reason string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("bug id is required")
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if items[i].InboxState == BugInboxHistory {
			return nil
		}
		items[i].InboxState = BugInboxHistory
		items[i].ArchivedAt = &now
		items[i].ArchiveReason = strings.TrimSpace(reason)
		return s.writeAll(items)
	}
	return os.ErrNotExist
}

// PruneStale 将指定来源+平台中不在 keepIDs 集合里的 Bug 移出收件箱。
// 历史快照不会删除，解决/关闭/重新指派后的工单仍可审计和查看。
// platformID 为空时退化为只按 source 匹配（兼容老数据没有 platform_id 的情况）。
func (s *Store) PruneStale(source, platformID string, keepIDs []string) (int, error) {
	prunedIDs, err := s.PruneStaleIDs(source, platformID, keepIDs)
	if err != nil {
		return 0, err
	}
	return len(prunedIDs), nil
}

// PruneStaleIDs is PruneStale 的可观测变体，返回本次新归档的 Bug ID。
func (s *Store) PruneStaleIDs(source, platformID string, keepIDs []string) ([]string, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	prunedIDs := []string{}
	now := time.Now().UTC()
	for i := range items {
		item := &items[i]
		if item.Source == source {
			if platformID == "" || item.PlatformID == "" || item.PlatformID == platformID {
				if !keep[item.ID] && item.InboxState != BugInboxHistory {
					prunedIDs = append(prunedIDs, item.ID)
					item.InboxState = BugInboxHistory
					item.ArchivedAt = &now
					item.ArchiveReason = BugArchiveNoLongerAssigned
				}
			}
		}
	}
	if len(prunedIDs) == 0 {
		return nil, nil
	}
	if err := s.writeAll(items); err != nil {
		return nil, err
	}
	return prunedIDs, nil
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
		if bugSourceResolutionComplete(items[i].Status) {
			items[i].InboxState = BugInboxHistory
			if items[i].ArchivedAt == nil && !items[i].UpdatedAt.IsZero() {
				archivedAt := items[i].UpdatedAt
				items[i].ArchivedAt = &archivedAt
			}
			if strings.TrimSpace(items[i].ArchiveReason) == "" {
				items[i].ArchiveReason = BugArchiveSourceResolved
			}
		} else if items[i].InboxState != BugInboxHistory {
			items[i].InboxState = BugInboxActive
		}
		if items[i].Source == "zentao" {
			items[i].Steps = zentaoHTMLToText(items[i].Steps)
			items[i].Description = zentaoHTMLToText(items[i].Description)
			items[i].Expected = zentaoHTMLToText(items[i].Expected)
			items[i].Actual = zentaoHTMLToText(items[i].Actual)
		}
	}
}

func bugSourceResolutionComplete(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "closed":
		return true
	default:
		return false
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
