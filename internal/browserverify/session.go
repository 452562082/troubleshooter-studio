package browserverify

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxBrowserSessionBytes = 16 << 20
const maxEncryptedBrowserSessionBytes = ((maxBrowserSessionBytes + aes.BlockSize + 2) / 3 * 4) + 4096

var ErrSecretNotFound = errors.New("browser session key not found")

var ErrSessionStoreBusy = errors.New("browser session store is busy")

var errSecretStoreUnavailable = errors.New("browser session key store unavailable")
var errSessionLockOwnershipLost = errors.New("browser session transaction lock ownership was lost")

type SecretStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

type SessionKey struct {
	SystemID    string
	Environment string
	Origin      string
}

type SessionStore struct {
	root    string
	secrets SecretStore
	mu      sync.Mutex
	memory  map[string][]byte
	// afterPublish is a test-only fault injection point after ciphertext rename
	// and before the remaining durability/cleanup steps.
	afterPublish func(string) error
}

type encryptedSessionEnvelope struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type sessionFileLock struct {
	path string
	file *os.File
	info os.FileInfo
}

func NewSessionStore(root string, secrets SecretStore) *SessionStore {
	return &SessionStore{
		root:    root,
		secrets: secrets,
		memory:  make(map[string][]byte),
	}
}

func (s *SessionStore) Load(key SessionKey) (state []byte, found bool, returnedErr error) {
	identifier, err := sessionIdentifier(key)
	if err != nil {
		return nil, false, err
	}

	// Every operation takes locks in the same order: the instance mutex first,
	// then the cross-process identifier lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireSessionFileLockForLoad(s.root, identifier)
	if err != nil {
		return nil, false, err
	}
	defer func() { returnedErr = errors.Join(returnedErr, lock.release()) }()
	if cached, ok := s.memory[identifier]; ok {
		return bytes.Clone(cached), true, nil
	}

	path := filepath.Join(s.root, identifier+".json")
	envelope, found, err := readEncryptedSessionEnvelope(path)
	if err != nil || !found {
		return nil, false, err
	}
	if s.secrets == nil {
		return nil, false, errSecretStoreUnavailable
	}
	encodedKey, err := s.secrets.Get(identifier)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, false, errors.New("encrypted browser session has no encryption key")
		}
		return nil, false, errSecretStoreUnavailable
	}
	aesKey, err := decodeSessionAESKey(encodedKey)
	if err != nil {
		return nil, false, err
	}
	state, err = decryptSessionState(identifier, aesKey, envelope)
	if err != nil {
		return nil, false, err
	}
	return bytes.Clone(state), true, nil
}

func (s *SessionStore) Save(key SessionKey, state []byte) (returnedErr error) {
	identifier, err := sessionIdentifier(key)
	if err != nil {
		return err
	}
	if len(state) > maxBrowserSessionBytes {
		return fmt.Errorf("browser session exceeds %d bytes", maxBrowserSessionBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireSessionFileLock(s.root, identifier)
	if err != nil {
		return err
	}
	defer func() { returnedErr = errors.Join(returnedErr, lock.release()) }()
	if s.secrets == nil {
		s.memory[identifier] = bytes.Clone(state)
		return nil
	}

	encodedKey, err := s.secrets.Get(identifier)
	if err != nil {
		if !errors.Is(err, ErrSecretNotFound) {
			s.memory[identifier] = bytes.Clone(state)
			return nil
		}
		aesKey := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
			return errors.New("generate browser session encryption key")
		}
		encodedKey = base64.StdEncoding.EncodeToString(aesKey)
		if err := s.secrets.Set(identifier, encodedKey); err != nil {
			s.memory[identifier] = bytes.Clone(state)
			return nil
		}
	}

	aesKey, err := decodeSessionAESKey(encodedKey)
	if err != nil {
		return err
	}
	envelope, err := encryptSessionState(identifier, aesKey, state)
	if err != nil {
		return err
	}
	if err := s.persist(identifier, envelope); err != nil {
		return err
	}
	delete(s.memory, identifier)
	return nil
}

func (s *SessionStore) Clear(key SessionKey) (returnedErr error) {
	identifier, err := sessionIdentifier(key)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireSessionFileLock(s.root, identifier)
	if err != nil {
		return err
	}
	defer func() { returnedErr = errors.Join(returnedErr, lock.release()) }()
	delete(s.memory, identifier)
	path := filepath.Join(s.root, identifier+".json")
	removed := false
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return errors.New("remove encrypted browser session")
		}
	} else {
		removed = true
	}
	if removed {
		if err := syncRuntimeDirectory(s.root); err != nil {
			return errors.New("sync browser session directory")
		}
	}
	if s.secrets == nil {
		return errSecretStoreUnavailable
	}
	if err := s.secrets.Delete(identifier); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return errSecretStoreUnavailable
	}
	return nil
}

func acquireSessionFileLock(root, identifier string) (_ *sessionFileLock, returnedErr error) {
	return acquireSessionFileLockWithDirectoryPolicy(root, identifier, true)
}

func acquireSessionFileLockForLoad(root, identifier string) (_ *sessionFileLock, returnedErr error) {
	return acquireSessionFileLockWithDirectoryPolicy(root, identifier, false)
}

func acquireSessionFileLockWithDirectoryPolicy(root, identifier string, protectDirectory bool) (_ *sessionFileLock, returnedErr error) {
	if err := prepareSessionLockDirectory(root, protectDirectory); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "."+identifier+".lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, ErrSessionStoreBusy
	}
	if err != nil {
		return nil, errors.New("create browser session transaction lock")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errors.New("inspect browser session transaction lock")
	}
	lock := &sessionFileLock{path: path, file: file, info: info}
	remove := true
	defer func() {
		if remove {
			returnedErr = errors.Join(returnedErr, lock.release())
		}
	}()
	token := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return nil, errors.New("generate browser session transaction token")
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.New("protect browser session transaction lock")
	}
	if err := writeAll(file, append([]byte(hex.EncodeToString(token)), '\n')); err != nil {
		return nil, errors.New("write browser session transaction lock")
	}
	if err := file.Sync(); err != nil {
		return nil, errors.New("sync browser session transaction lock")
	}
	if err := syncRuntimeDirectory(root); err != nil {
		return nil, errors.New("sync browser session directory")
	}
	remove = false
	return lock, nil
}

func (lock *sessionFileLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	openedInfo, openedErr := lock.file.Stat()
	pathInfo, pathErr := os.Lstat(lock.path)
	if openedErr != nil || pathErr != nil || !os.SameFile(lock.info, openedInfo) || !os.SameFile(openedInfo, pathInfo) {
		_ = lock.file.Close()
		lock.file = nil
		return errSessionLockOwnershipLost
	}
	closeErr := lock.file.Close()
	lock.file = nil
	if closeErr != nil {
		return errors.New("close browser session transaction lock")
	}
	if err := os.Remove(lock.path); err != nil {
		return errors.New("remove browser session transaction lock")
	}
	if err := syncRuntimeDirectory(filepath.Dir(lock.path)); err != nil {
		return errors.New("sync browser session directory")
	}
	return nil
}

func prepareSessionLockDirectory(root string, protect bool) error {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return errors.New("browser session directory must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return errors.New("create browser session directory")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("browser session directory is unsafe")
	}
	if protect {
		if err := os.Chmod(root, 0o700); err != nil {
			return errors.New("protect browser session directory")
		}
		info, err = os.Lstat(root)
	}
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return errors.New("browser session directory is unsafe")
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		return errors.New("protect browser session directory")
	}
	return nil
}

func (s *SessionStore) encryptedPath(key SessionKey) string {
	identifier, err := sessionIdentifier(key)
	if err != nil {
		digest := sha256.Sum256([]byte(key.SystemID + "\x00" + key.Environment + "\x00" + key.Origin))
		identifier = hex.EncodeToString(digest[:])
	}
	return filepath.Join(s.root, identifier+".json")
}

func sessionIdentifier(key SessionKey) (string, error) {
	if strings.TrimSpace(key.SystemID) == "" || strings.TrimSpace(key.Environment) == "" ||
		strings.ContainsRune(key.SystemID, '\x00') || strings.ContainsRune(key.Environment, '\x00') {
		return "", errors.New("browser session identity is invalid")
	}
	origin, err := canonicalSessionOrigin(key.Origin)
	if err != nil {
		return "", errors.New("browser session origin is invalid")
	}
	digest := sha256.Sum256([]byte(key.SystemID + "\x00" + key.Environment + "\x00" + origin))
	return hex.EncodeToString(digest[:]), nil
}

func canonicalSessionOrigin(raw string) (string, error) {
	parsed, normalizedOrigin, host, err := parseBrowserURL(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(normalizedOrigin, scheme+"://"))
	if err != nil {
		return "", err
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return scheme + "://" + host, nil
	}
	return normalizedOrigin, nil
}

func decodeSessionAESKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("browser session encryption key is invalid")
	}
	return key, nil
}

func encryptSessionState(identifier string, key, state []byte) (encryptedSessionEnvelope, error) {
	gcm, err := newSessionGCM(key)
	if err != nil {
		return encryptedSessionEnvelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return encryptedSessionEnvelope{}, errors.New("generate browser session nonce")
	}
	ciphertext := gcm.Seal(nil, nonce, state, []byte(identifier))
	return encryptedSessionEnvelope{
		Version:    1,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func decryptSessionState(identifier string, key []byte, envelope encryptedSessionEnvelope) ([]byte, error) {
	if envelope.Version != 1 {
		return nil, errors.New("browser session encryption version is invalid")
	}
	gcm, err := newSessionGCM(key)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, errors.New("browser session nonce is invalid")
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) < gcm.Overhead() {
		return nil, errors.New("browser session ciphertext is invalid")
	}
	state, err := gcm.Open(nil, nonce, ciphertext, []byte(identifier))
	if err != nil {
		return nil, errors.New("browser session authentication failed")
	}
	if len(state) > maxBrowserSessionBytes {
		return nil, errors.New("browser session plaintext exceeds its limit")
	}
	return state, nil
}

func newSessionGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("browser session encryption key is invalid")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize browser session encryption")
	}
	return gcm, nil
}

func (s *SessionStore) persist(identifier string, envelope encryptedSessionEnvelope) error {
	if err := ensureSessionDirectory(s.root); err != nil {
		return err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return errors.New("encode encrypted browser session")
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(s.root, "."+identifier+".json-*")
	if err != nil {
		return errors.New("create encrypted browser session")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("protect encrypted browser session")
	}
	writeErr := writeAll(temporary, encoded)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.New("write encrypted browser session")
	}
	path := filepath.Join(s.root, identifier+".json")
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("publish encrypted browser session")
	}
	if s.afterPublish != nil {
		if err := s.afterPublish(path); err != nil {
			return err
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return errors.New("protect encrypted browser session")
	}
	if err := syncRuntimeDirectory(s.root); err != nil {
		return errors.New("sync browser session directory")
	}
	return nil
}

func ensureSessionDirectory(root string) error {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return errors.New("browser session directory must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return errors.New("create browser session directory")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("browser session directory is unsafe")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return errors.New("protect browser session directory")
	}
	return nil
}

func readEncryptedSessionEnvelope(path string) (encryptedSessionEnvelope, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return encryptedSessionEnvelope{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxEncryptedBrowserSessionBytes {
		return encryptedSessionEnvelope{}, true, errors.New("encrypted browser session file is unsafe")
	}
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o077 != 0 {
		return encryptedSessionEnvelope{}, true, errors.New("encrypted browser session directory is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return encryptedSessionEnvelope{}, true, errors.New("open encrypted browser session")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return encryptedSessionEnvelope{}, true, errors.New("encrypted browser session changed while opening")
	}
	var envelope encryptedSessionEnvelope
	decoder := json.NewDecoder(io.LimitReader(file, maxEncryptedBrowserSessionBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return encryptedSessionEnvelope{}, true, errors.New("encrypted browser session envelope is invalid")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return encryptedSessionEnvelope{}, true, errors.New("encrypted browser session envelope is invalid")
	}
	return envelope, true, nil
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}
