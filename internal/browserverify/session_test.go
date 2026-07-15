package browserverify

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memorySecretStore struct {
	values    map[string]string
	beforeGet func()
}

func newMemorySecretStore() *memorySecretStore {
	return &memorySecretStore{values: map[string]string{}}
}

func (s *memorySecretStore) Get(key string) (string, error) {
	if s.beforeGet != nil {
		s.beforeGet()
	}
	value, ok := s.values[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func (s *memorySecretStore) Set(key, value string) error {
	s.values[key] = value
	return nil
}

func (s *memorySecretStore) Delete(key string) error {
	delete(s.values, key)
	return nil
}

type failingSecretStore struct{}

func (failingSecretStore) Get(string) (string, error) {
	return "", errors.New("keyring unavailable")
}

func (failingSecretStore) Set(string, string) error {
	return errors.New("keyring unavailable")
}

func (failingSecretStore) Delete(string) error {
	return errors.New("keyring unavailable")
}

type setFailingSecretStore struct {
	setCalls int
}

func (*setFailingSecretStore) Get(string) (string, error) { return "", ErrSecretNotFound }
func (s *setFailingSecretStore) Set(string, string) error {
	s.setCalls++
	return errors.New("keyring unavailable")
}
func (*setFailingSecretStore) Delete(string) error { return nil }

type deleteFailingSecretStore struct {
	*memorySecretStore
	deleteErr error
}

func (s *deleteFailingSecretStore) Delete(string) error { return s.deleteErr }

type blockingDeleteSecretStore struct {
	*memorySecretStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingDeleteSecretStore) Delete(key string) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.memorySecretStore.Delete(key)
}

func TestSessionStoreSerializesTwoStoreSaveTransactions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	secrets := newMemorySecretStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	secrets.beforeGet = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	first := NewSessionStore(root, secrets)
	second := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	firstState := []byte(`{"cookies":[{"value":"first"}]}`)
	secondState := []byte(`{"cookies":[{"value":"second"}]}`)

	firstResult := make(chan error, 1)
	go func() { firstResult <- first.Save(key, firstState) }()
	<-entered
	if err := second.Save(key, secondState); !errors.Is(err, ErrSessionStoreBusy) {
		t.Fatalf("concurrent Save error = %v, want busy", err)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	secrets.beforeGet = nil
	loaded, ok, err := second.Load(key)
	if err != nil || !ok || !bytes.Equal(loaded, firstState) {
		t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err)
	}
}

func TestSessionStoreClearExcludesConcurrentSaveAcrossStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	secrets := &blockingDeleteSecretStore{
		memorySecretStore: newMemorySecretStore(),
		started:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	first := NewSessionStore(root, secrets)
	second := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	if err := first.Save(key, []byte(`{"cookies":[{"value":"old"}]}`)); err != nil {
		t.Fatal(err)
	}

	clearResult := make(chan error, 1)
	go func() { clearResult <- first.Clear(key) }()
	<-secrets.started
	if err := second.Save(key, []byte(`{"cookies":[{"value":"must-not-reappear"}]}`)); !errors.Is(err, ErrSessionStoreBusy) {
		t.Fatalf("Save during Clear error = %v, want busy", err)
	}
	close(secrets.release)
	if err := <-clearResult; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.encryptedPath(key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext reappeared after Clear: %v", err)
	}
	if _, ok, err := second.Load(key); err != nil || ok {
		t.Fatalf("load after Clear: ok=%v err=%v", ok, err)
	}
}

func TestSessionFileLockReleaseDoesNotDeleteReplacementOwnedByAnotherProcess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	identifier := strings.Repeat("a", 64)
	lock, err := acquireSessionFileLock(root, identifier)
	if err != nil {
		t.Fatal(err)
	}
	displaced := lock.path + ".displaced"
	if err := os.Rename(lock.path, displaced); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign-owner\n")
	if err := os.WriteFile(lock.path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.release(); !errors.Is(err, errSessionLockOwnershipLost) {
		t.Fatalf("release error = %v, want ownership loss", err)
	}
	contents, err := os.ReadFile(lock.path)
	if err != nil || !bytes.Equal(contents, foreign) {
		t.Fatalf("replacement lock deleted or changed: contents=%q err=%v", contents, err)
	}
}

func TestSessionStorePersistsOnlyCiphertext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	secrets := newMemorySecretStore()
	store := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test/users?token=do-not-persist"}
	state := []byte(`{"cookies":[{"name":"sid","value":"plain-secret"}]}`)
	if err := store.Save(key, state); err != nil {
		t.Fatal(err)
	}

	path := store.encryptedPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"plain-secret", "app.test", "token", "base", "test"} {
		if bytes.Contains(data, []byte(forbidden)) || strings.Contains(path, forbidden) {
			t.Fatalf("session storage exposed %q: path=%q data=%q", forbidden, path, data)
		}
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 3 || envelope["version"] != float64(1) || envelope["nonce"] == "" || envelope["ciphertext"] == "" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if mode := mustFileMode(t, root); mode != 0o700 {
		t.Fatalf("session directory mode = %o", mode)
	}
	if mode := mustFileMode(t, path); mode != 0o600 {
		t.Fatalf("session file mode = %o", mode)
	}

	loaded, ok, err := store.Load(key)
	if err != nil || !ok || !bytes.Equal(loaded, state) {
		t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err)
	}
}

func TestSessionStoreUsesCanonicalOriginHashAsSecretAndFileIdentifier(t *testing.T) {
	root := t.TempDir()
	secrets := newMemorySecretStore()
	store := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "HTTPS://APP.TEST:443/path?secret=value"}
	if err := store.Save(key, []byte(`{"cookies":[]}`)); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte("base\x00test\x00https://app.test"))
	wantID := hex.EncodeToString(wantDigest[:])
	if _, ok := secrets.values[wantID]; !ok {
		t.Fatalf("secret keys = %#v, want canonical hash %q", secrets.values, wantID)
	}
	if got := filepath.Base(store.encryptedPath(key)); got != wantID+".json" {
		t.Fatalf("encrypted path base = %q, want %q", got, wantID+".json")
	}
	equivalent := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test:0443/other"}
	if got := filepath.Base(store.encryptedPath(equivalent)); got != wantID+".json" {
		t.Fatalf("zero-padded default port path = %q, want %q", got, wantID+".json")
	}
}

func TestSessionStoreFallsBackToClonedMemoryWhenKeyringUnavailable(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root, failingSecretStore{})
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	state := []byte(`{"cookies":[{"value":"memory-secret"}]}`)
	if err := store.Save(key, state); err != nil {
		t.Fatal(err)
	}
	state[0] = 'X'
	if _, err := os.Stat(store.encryptedPath(key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session persisted without keyring: %v", err)
	}
	loaded, ok, err := store.Load(key)
	if err != nil || !ok || string(loaded) != `{"cookies":[{"value":"memory-secret"}]}` {
		t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err)
	}
	loaded[0] = 'Y'
	again, ok, err := store.Load(key)
	if err != nil || !ok || string(again) != `{"cookies":[{"value":"memory-secret"}]}` {
		t.Fatalf("second load=%q ok=%v err=%v", again, ok, err)
	}
}

func TestSessionStoreSetFailureUsesMemoryWithoutCiphertext(t *testing.T) {
	root := t.TempDir()
	secrets := &setFailingSecretStore{}
	store := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	state := []byte(`{"cookies":[]}`)
	if err := store.Save(key, state); err != nil {
		t.Fatal(err)
	}
	if secrets.setCalls != 1 {
		t.Fatalf("Set calls = %d", secrets.setCalls)
	}
	if _, err := os.Stat(store.encryptedPath(key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session persisted after Set failure: %v", err)
	}
	if loaded, ok, err := store.Load(key); err != nil || !ok || !bytes.Equal(loaded, state) {
		t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err)
	}
}

func TestSessionStoreLoadsMaximumSizedEncryptedState(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	state := bytes.Repeat([]byte{0xab}, maxBrowserSessionBytes)
	if err := store.Save(key, state); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load(key)
	if err != nil || !ok || !bytes.Equal(loaded, state) {
		t.Fatalf("loaded bytes=%d ok=%v err=%v", len(loaded), ok, err)
	}
}

func TestSessionStoreRejectsInvalidStoredKeyWithoutReplacingIt(t *testing.T) {
	for _, invalid := range []string{"not-base64", base64.StdEncoding.EncodeToString([]byte("short"))} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			secrets := newMemorySecretStore()
			key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
			identifier := sessionIdentifierForTest(key)
			secrets.values[identifier] = invalid
			store := NewSessionStore(root, secrets)
			if err := store.Save(key, []byte(`{"cookies":[]}`)); err == nil {
				t.Fatal("expected invalid key failure")
			}
			if secrets.values[identifier] != invalid {
				t.Fatal("invalid key was silently replaced")
			}
			if _, err := os.Stat(store.encryptedPath(key)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ciphertext created with invalid key: %v", err)
			}
		})
	}
}

func TestSessionStoreLoadFailsClosedOnCorruptCiphertext(t *testing.T) {
	cases := map[string]func(map[string]any){
		"version":       func(value map[string]any) { value["version"] = float64(2) },
		"nonce base64":  func(value map[string]any) { value["nonce"] = "%%%" },
		"nonce length":  func(value map[string]any) { value["nonce"] = base64.StdEncoding.EncodeToString([]byte("short")) },
		"cipher base64": func(value map[string]any) { value["ciphertext"] = "%%%" },
		"cipher tag":    func(value map[string]any) { value["ciphertext"] = base64.StdEncoding.EncodeToString([]byte("short")) },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := NewSessionStore(root, newMemorySecretStore())
			key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
			if err := store.Save(key, []byte(`{"cookies":[]}`)); err != nil {
				t.Fatal(err)
			}
			path := store.encryptedPath(key)
			var value map[string]any
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &value) != nil {
				t.Fatalf("read envelope: %v", err)
			}
			corrupt(value)
			data, err = json.Marshal(value)
			if err != nil || os.WriteFile(path, data, 0o600) != nil {
				t.Fatalf("write corrupt envelope: %v", err)
			}
			if _, ok, err := store.Load(key); err == nil || ok {
				t.Fatalf("ok=%v err=%v, want fail closed", ok, err)
			}
		})
	}
}

func TestSessionStoreLoadRejectsOverPermissiveCiphertextPermissions(t *testing.T) {
	for _, target := range []string{"file", "directory"} {
		t.Run(target, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			store := NewSessionStore(root, newMemorySecretStore())
			key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
			if err := store.Save(key, []byte(`{"cookies":[]}`)); err != nil {
				t.Fatal(err)
			}
			path := store.encryptedPath(key)
			if target == "directory" {
				path = root
			}
			if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := store.Load(key); err == nil || ok {
				t.Fatalf("ok=%v err=%v, want permission failure", ok, err)
			}
		})
	}
}

func TestSessionStoreClearDeletesCiphertextBeforeReportingKeyringFailure(t *testing.T) {
	root := t.TempDir()
	secrets := &deleteFailingSecretStore{memorySecretStore: newMemorySecretStore(), deleteErr: errors.New("keyring unavailable")}
	store := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	if err := store.Save(key, []byte(`{"cookies":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(key); err == nil {
		t.Fatal("expected keyring delete failure")
	}
	if _, err := os.Stat(store.encryptedPath(key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remains after clear: %v", err)
	}
	if _, ok, err := store.Load(key); err != nil || ok {
		t.Fatalf("load after unavailable keyring clear: ok=%v err=%v", ok, err)
	}
}

func TestSessionStoreClearIsIdempotentForMissingState(t *testing.T) {
	store := NewSessionStore(t.TempDir(), &deleteFailingSecretStore{
		memorySecretStore: newMemorySecretStore(),
		deleteErr:         ErrSecretNotFound,
	})
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	if err := store.Clear(key); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(key); err != nil {
		t.Fatal(err)
	}
}

func sessionIdentifierForTest(key SessionKey) string {
	_, origin, _, err := parseBrowserURL(key.Origin)
	if err != nil {
		panic(err)
	}
	// parseBrowserURL uses explicit default ports; the session identity uses the
	// web-origin spelling with default ports elided.
	origin = strings.TrimSuffix(strings.TrimSuffix(origin, ":443"), ":80")
	digest := sha256.Sum256([]byte(key.SystemID + "\x00" + key.Environment + "\x00" + origin))
	return hex.EncodeToString(digest[:])
}

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
