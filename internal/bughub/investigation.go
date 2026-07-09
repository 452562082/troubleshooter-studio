package bughub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type InvestigationStatus string

const (
	InvestigationQueued    InvestigationStatus = "queued"
	InvestigationRunning   InvestigationStatus = "running"
	InvestigationSucceeded InvestigationStatus = "succeeded"
	InvestigationFailed    InvestigationStatus = "failed"
	InvestigationCancelled InvestigationStatus = "cancelled"
)

type InvestigationEvent struct {
	At      time.Time      `json:"at,omitempty"`
	Type    string         `json:"type,omitempty"`
	Message string         `json:"message,omitempty"`
	Raw     any            `json:"raw,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type InvestigationRun struct {
	ID             string               `json:"id"`
	BugID          string               `json:"bug_id"`
	BotKey         string               `json:"bot_key,omitempty"`
	Status         InvestigationStatus  `json:"status"`
	StartedAt      time.Time            `json:"started_at,omitempty"`
	FinishedAt     *time.Time           `json:"finished_at,omitempty"`
	PromptPreview  string               `json:"prompt_preview,omitempty"`
	Events         []InvestigationEvent `json:"events,omitempty"`
	FinalMessage   string               `json:"final_message,omitempty"`
	Error          string               `json:"error,omitempty"`
	ContinuationOf string               `json:"continuation_of,omitempty"`
}

var investigationStoreMu sync.Mutex

type InvestigationStore struct {
	root string
}

func NewInvestigationStore(root string) *InvestigationStore {
	return &InvestigationStore{root: root}
}

func (s *InvestigationStore) Path() string {
	return filepath.Join(s.root, "runs.json")
}

func (s *InvestigationStore) ListByBug(bugID string) ([]InvestigationRun, error) {
	investigationStoreMu.Lock()
	defer investigationStoreMu.Unlock()
	return s.listByBugLocked(bugID)
}

func (s *InvestigationStore) listByBugLocked(bugID string) ([]InvestigationRun, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	var runs []InvestigationRun
	for _, item := range items {
		if item.BugID == bugID {
			runs = append(runs, item)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	return runs, nil
}

func (s *InvestigationStore) ActiveRunForBug(bugID string) (InvestigationRun, bool, error) {
	investigationStoreMu.Lock()
	defer investigationStoreMu.Unlock()
	runs, err := s.listByBugLocked(bugID)
	if err != nil {
		return InvestigationRun{}, false, err
	}
	for _, run := range runs {
		if run.Status == InvestigationQueued || run.Status == InvestigationRunning {
			return run, true, nil
		}
	}
	return InvestigationRun{}, false, nil
}

func (s *InvestigationStore) Get(runID string) (InvestigationRun, error) {
	investigationStoreMu.Lock()
	defer investigationStoreMu.Unlock()
	items, err := s.readAll()
	if err != nil {
		return InvestigationRun{}, err
	}
	for _, item := range items {
		if item.ID == runID {
			return item, nil
		}
	}
	return InvestigationRun{}, os.ErrNotExist
}

func (s *InvestigationStore) Upsert(run InvestigationRun) error {
	investigationStoreMu.Lock()
	defer investigationStoreMu.Unlock()
	if strings.TrimSpace(run.ID) == "" {
		return errors.New("investigation run id is required")
	}
	if strings.TrimSpace(run.BugID) == "" {
		return errors.New("investigation run bug id is required")
	}
	now := time.Now().UTC()
	if run.Status == "" {
		run.Status = InvestigationQueued
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	replaced := false
	for i := range items {
		if items[i].ID == run.ID {
			items[i] = run
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, run)
	}
	return s.writeAll(items)
}

func (s *InvestigationStore) AppendEvent(runID string, event InvestigationEvent) error {
	investigationStoreMu.Lock()
	defer investigationStoreMu.Unlock()
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID == runID {
			if event.At.IsZero() {
				event.At = time.Now().UTC()
			}
			items[i].Events = append(items[i].Events, event)
			return s.writeAll(items)
		}
	}
	return os.ErrNotExist
}

func (s *InvestigationStore) Finish(runID string, status InvestigationStatus, finalMessage, errorText string) error {
	investigationStoreMu.Lock()
	defer investigationStoreMu.Unlock()
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID == runID {
			finishedAt := time.Now().UTC()
			items[i].Status = status
			items[i].FinishedAt = &finishedAt
			items[i].FinalMessage = finalMessage
			items[i].Error = errorText
			return s.writeAll(items)
		}
	}
	return os.ErrNotExist
}

func (s *InvestigationStore) readAll() ([]InvestigationRun, error) {
	data, err := os.ReadFile(s.Path())
	if os.IsNotExist(err) {
		return []InvestigationRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []InvestigationRun{}, nil
	}
	var items []InvestigationRun
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return []InvestigationRun{}, nil
	}
	return items, nil
}

func (s *InvestigationStore) writeAll(items []InvestigationRun) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.Path(), append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(s.Path(), 0o600)
}
