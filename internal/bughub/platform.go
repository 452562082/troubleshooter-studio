package bughub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PlatformConfig struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	Type                string               `json:"type"`
	BaseURL             string               `json:"base_url,omitempty"`
	Account             string               `json:"account,omitempty"`
	Env                 string               `json:"env,omitempty"`
	AuthMode            string               `json:"auth_mode,omitempty"`
	SessionHeader       string               `json:"session_header,omitempty"`
	Password            string               `json:"password,omitempty"`
	Token               string               `json:"token,omitempty"`
	HookSecret          string               `json:"hook_secret,omitempty"`
	BotEnv              string               `json:"bot_env,omitempty"`
	BotMappings         []PlatformBotMapping `json:"bot_mappings,omitempty"`
	Enabled             bool                 `json:"enabled"`
	PollEnabled         bool                 `json:"poll_enabled,omitempty"`
	PollIntervalMinutes int                  `json:"poll_interval_minutes,omitempty"`
	CreatedAt           time.Time            `json:"created_at,omitempty"`
	UpdatedAt           time.Time            `json:"updated_at,omitempty"`
}

type PlatformBotMapping struct {
	BotKey string `json:"bot_key"`
	Env    string `json:"env,omitempty"`
}

type PlatformStore struct {
	root string
}

func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".tshoot", "bugs")
	}
	return filepath.Join(home, ".tshoot", "bugs")
}

func NewPlatformStore(root string) *PlatformStore {
	return &PlatformStore{root: root}
}

func (s *PlatformStore) Path() string {
	return filepath.Join(s.root, "platforms.json")
}

func (s *PlatformStore) List() ([]PlatformConfig, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s *PlatformStore) Get(id string) (PlatformConfig, bool, error) {
	items, err := s.readAll()
	if err != nil {
		return PlatformConfig{}, false, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return PlatformConfig{}, false, nil
}

func (s *PlatformStore) Upsert(p PlatformConfig) (PlatformConfig, error) {
	inputCreatedAtZero := p.CreatedAt.IsZero()
	inputAuthModeEmpty := strings.TrimSpace(p.AuthMode) == ""
	inputSessionHeaderEmpty := strings.TrimSpace(p.SessionHeader) == ""
	inputTokenEmpty := strings.TrimSpace(p.Token) == ""
	inputPasswordEmpty := strings.TrimSpace(p.Password) == ""
	inputSecretEmpty := strings.TrimSpace(p.HookSecret) == ""
	p.ID = slugify(p.ID)
	if p.ID == "" {
		p.ID = slugify(p.Name)
	}
	if p.ID == "" {
		p.ID = "bug-platform-" + randomHex(4)
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return PlatformConfig{}, errors.New("platform name is required")
	}
	p.Type = strings.TrimSpace(strings.ToLower(p.Type))
	if p.Type == "" {
		p.Type = "zentao"
	}
	p.Env = strings.TrimSpace(p.Env)
	p.AuthMode = strings.TrimSpace(strings.ToLower(p.AuthMode))
	if p.AuthMode == "" {
		p.AuthMode = "feishu_sso"
	}
	p.BotEnv = strings.TrimSpace(p.BotEnv)
	p.BotMappings = normalizePlatformBotMappings(p.BotMappings)
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	items, err := s.readAll()
	if err != nil {
		return PlatformConfig{}, err
	}
	replaced := false
	for i := range items {
		if items[i].ID == p.ID {
			if inputCreatedAtZero {
				p.CreatedAt = items[i].CreatedAt
			}
			if inputAuthModeEmpty {
				p.AuthMode = items[i].AuthMode
			}
			if inputSessionHeaderEmpty {
				p.SessionHeader = items[i].SessionHeader
			}
			if inputTokenEmpty {
				p.Token = items[i].Token
			}
			if inputPasswordEmpty {
				p.Password = items[i].Password
			}
			if inputSecretEmpty {
				p.HookSecret = items[i].HookSecret
			}
			items[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		if inputSecretEmpty {
			p.HookSecret = randomHex(16)
		}
		items = append(items, p)
	}
	if err := s.writeAll(items); err != nil {
		return PlatformConfig{}, err
	}
	return p, nil
}

func normalizePlatformBotMappings(items []PlatformBotMapping) []PlatformBotMapping {
	out := make([]PlatformBotMapping, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		key := strings.TrimSpace(item.BotKey)
		if key == "" || seen[key] || !SupportsIncidentWorkflowTarget(incidentWorkflowTargetFromBotKey(key)) {
			continue
		}
		seen[key] = true
		out = append(out, PlatformBotMapping{
			BotKey: key,
			Env:    strings.TrimSpace(item.Env),
		})
	}
	return out
}

func (s *PlatformStore) Delete(id string) error {
	id = slugify(id)
	if id == "" {
		return errors.New("platform id is required")
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	next := items[:0]
	deleted := false
	for _, item := range items {
		if item.ID == id {
			deleted = true
			continue
		}
		next = append(next, item)
	}
	if !deleted {
		return os.ErrNotExist
	}
	return s.writeAll(next)
}

func (s *PlatformStore) SetSessionHeader(id string, authMode string, sessionHeader string) (PlatformConfig, error) {
	id = slugify(id)
	sessionHeader = strings.TrimSpace(sessionHeader)
	if id == "" {
		return PlatformConfig{}, errors.New("platform id is required")
	}
	if sessionHeader == "" {
		return PlatformConfig{}, errors.New("session header is required")
	}
	items, err := s.readAll()
	if err != nil {
		return PlatformConfig{}, err
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		items[i].AuthMode = strings.TrimSpace(strings.ToLower(authMode))
		if items[i].AuthMode == "" {
			items[i].AuthMode = "feishu_sso"
		}
		items[i].SessionHeader = sessionHeader
		items[i].UpdatedAt = time.Now().UTC()
		if err := s.writeAll(items); err != nil {
			return PlatformConfig{}, err
		}
		return items[i], nil
	}
	return PlatformConfig{}, os.ErrNotExist
}

func (s *PlatformStore) ClearSessionHeader(id string) (PlatformConfig, error) {
	id = slugify(id)
	if id == "" {
		return PlatformConfig{}, errors.New("platform id is required")
	}
	items, err := s.readAll()
	if err != nil {
		return PlatformConfig{}, err
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		items[i].SessionHeader = ""
		items[i].UpdatedAt = time.Now().UTC()
		if err := s.writeAll(items); err != nil {
			return PlatformConfig{}, err
		}
		return items[i], nil
	}
	return PlatformConfig{}, os.ErrNotExist
}

func (s *PlatformStore) readAll() ([]PlatformConfig, error) {
	data, err := os.ReadFile(s.Path())
	if os.IsNotExist(err) {
		return []PlatformConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return []PlatformConfig{}, nil
	}
	var items []PlatformConfig
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return []PlatformConfig{}, nil
	}
	return items, nil
}

func (s *PlatformStore) writeAll(items []PlatformConfig) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(), append(data, '\n'), 0o600)
}

func slugify(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return hex.EncodeToString(buf)
}
