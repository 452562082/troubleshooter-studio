//go:build !windows

package bughub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type unixAttemptEvidenceStaging struct {
	path     string
	name     string
	fd       int
	parentFD int
}

func openAttemptEvidenceStaging(root, attemptID string) (attemptEvidenceStaging, error) {
	if err := validateArtifactComponent("attempt ID", attemptID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("evidence staging root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(absRoot) == filepath.Clean(filepath.VolumeName(absRoot)+string(filepath.Separator)) {
		return nil, errors.New("evidence staging root must be a dedicated subdirectory")
	}
	rootFD, err := openOrCreateDirectoryPath(absRoot)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	stagingFD, err := openOrCreateDirectoryAt(rootFD, ".staging")
	if err != nil {
		return nil, err
	}
	defer unix.Close(stagingFD)
	if err := unix.Fchmod(stagingFD, 0o700); err != nil {
		return nil, fmt.Errorf("secure evidence staging root: %w", err)
	}
	for tries := 0; tries < 100; tries++ {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		name := attemptID + "-" + hex.EncodeToString(nonce[:])
		if err := unix.Mkdirat(stagingFD, name, 0o700); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("create attempt evidence staging: %w", err)
		}
		fd, err := unix.Openat(stagingFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("open attempt evidence staging: %w", err)
		}
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		duplicate, err := unix.Dup(fd)
		if err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		file := os.NewFile(uintptr(duplicate), name)
		entries, readErr := file.Readdirnames(1)
		_ = file.Close()
		if readErr != io.EOF || len(entries) != 0 {
			_ = unix.Close(fd)
			return nil, errors.New("new attempt evidence staging directory is not empty")
		}
		parentFD, err := unix.Dup(stagingFD)
		if err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		return &unixAttemptEvidenceStaging{path: filepath.Join(absRoot, ".staging", name), name: name, fd: fd, parentFD: parentFD}, nil
	}
	return nil, errors.New("create unique attempt evidence staging directory")
}

func openExistingAttemptEvidenceStaging(root, attemptID, locator string) (attemptEvidenceStaging, error) {
	if err := validateArtifactComponent("attempt ID", attemptID); err != nil {
		return nil, err
	}
	if err := validateArtifactComponent("fix checkpoint locator", locator); err != nil || !strings.HasPrefix(locator, attemptID+"-") {
		return nil, errors.New("fix checkpoint locator is not bound to its attempt")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootFD, err := openOrCreateDirectoryPath(absRoot)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	stagingFD, err := unix.Openat(rootFD, ".staging", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open fix checkpoint staging root: %w", err)
	}
	defer unix.Close(stagingFD)
	fd, err := unix.Openat(stagingFD, locator, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open fix checkpoint staging directory: %w", err)
	}
	parentFD, err := unix.Dup(stagingFD)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &unixAttemptEvidenceStaging{path: filepath.Join(absRoot, ".staging", locator), name: locator, fd: fd, parentFD: parentFD}, nil
}

func sweepTerminalFixStaging(root string, terminalAttemptIDs []string) error {
	if len(terminalAttemptIDs) == 0 {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootFD, err := openOrCreateDirectoryPath(absRoot)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	stagingFD, err := unix.Openat(rootFD, ".staging", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(stagingFD)
	duplicate, err := unix.Dup(stagingFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(duplicate), ".staging")
	entries, readErr := directory.ReadDir(-1)
	_ = directory.Close()
	if readErr != nil {
		return readErr
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		for _, attemptID := range terminalAttemptIDs {
			if !strings.HasPrefix(entry.Name(), attemptID+"-") {
				continue
			}
			staging, openErr := openExistingAttemptEvidenceStaging(root, attemptID, entry.Name())
			if openErr != nil {
				return openErr
			}
			cleanupErr := staging.Cleanup()
			closeErr := staging.Close()
			if cleanupErr != nil || closeErr != nil {
				return errors.Join(cleanupErr, closeErr)
			}
			break
		}
	}
	return nil
}

func createAttemptEvidenceStaging(root, attemptID string) (string, error) {
	staging, err := openAttemptEvidenceStaging(root, attemptID)
	if err != nil {
		return "", err
	}
	path := staging.Path()
	return path, staging.Close()
}

func (s *unixAttemptEvidenceStaging) Path() string { return s.path }

func (s *unixAttemptEvidenceStaging) Capture(relative string) (capturedArtifactSource, error) {
	if s == nil || s.fd < 0 {
		return capturedArtifactSource{}, errors.New("attempt evidence staging is closed")
	}
	fd, err := unix.Dup(s.fd)
	if err != nil {
		return capturedArtifactSource{}, err
	}
	return captureAttemptStagedArtifactFromFD(fd, relative)
}

func (s *unixAttemptEvidenceStaging) Close() error {
	if s == nil {
		return nil
	}
	var first error
	if s.fd >= 0 {
		first = unix.Close(s.fd)
		s.fd = -1
	}
	if s.parentFD >= 0 {
		if err := unix.Close(s.parentFD); first == nil {
			first = err
		}
		s.parentFD = -1
	}
	return first
}

func (s *unixAttemptEvidenceStaging) Cleanup() error {
	if s == nil || s.fd < 0 || s.parentFD < 0 {
		return errors.New("attempt evidence staging is closed")
	}
	if err := cleanupStagingDirectory(s.fd); err != nil {
		return err
	}
	currentFD, err := unix.Openat(s.parentFD, s.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("reopen attempt staging for cleanup: %w", err)
	}
	currentInfo, currentErr := fileInfoFromFD(currentFD, s.name)
	originalInfo, originalErr := fileInfoFromFD(s.fd, s.name)
	_ = unix.Close(currentFD)
	if currentErr != nil || originalErr != nil || !os.SameFile(currentInfo, originalInfo) {
		return errors.New("attempt evidence staging path changed before cleanup")
	}
	if err := unix.Unlinkat(s.parentFD, s.name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove attempt evidence staging: %w", err)
	}
	return nil
}

func cleanupStagingDirectory(fd int) error {
	for pass := 0; pass < 20; pass++ {
		if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
			return err
		}
		duplicate, err := unix.Dup(fd)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(duplicate), "staging")
		names, readErr := file.Readdirnames(-1)
		_ = file.Close()
		if readErr != nil {
			return readErr
		}
		if len(names) == 0 {
			return nil
		}
		for _, name := range names {
			if name == "" || name == "." || name == ".." || strings.Contains(name, string(filepath.Separator)) {
				return errors.New("invalid entry in attempt evidence staging")
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
				continue
			} else if err != nil {
				return err
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
				child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
				if err != nil {
					return err
				}
				err = cleanupStagingDirectory(child)
				_ = unix.Close(child)
				if err != nil {
					return err
				}
				if err := unix.Unlinkat(fd, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
					return err
				}
			} else if err := unix.Unlinkat(fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
	}
	return errors.New("attempt evidence staging changed continuously during cleanup")
}

func captureAttemptStagedArtifact(stagingDir, relative string) (capturedArtifactSource, error) {
	current, err := openDirectoryPath(stagingDir)
	if err != nil {
		return capturedArtifactSource{}, err
	}
	return captureAttemptStagedArtifactFromFD(current, relative)
}

func captureAttemptStagedArtifactFromFD(current int, relative string) (capturedArtifactSource, error) {
	if filepath.IsAbs(relative) || relative == "" || filepath.Clean(relative) != relative || strings.Contains(relative, `\`) {
		_ = unix.Close(current)
		return capturedArtifactSource{}, errors.New("evidence path must be a clean relative path inside the Studio staging directory")
	}
	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(current)
			return capturedArtifactSource{}, errors.New("evidence path escapes the Studio staging directory")
		}
	}
	for _, component := range components[:len(components)-1] {
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = unix.Close(current)
			return capturedArtifactSource{}, fmt.Errorf("open staged evidence ancestor without following links: %w", err)
		}
		_ = unix.Close(current)
		current = next
	}
	fd, err := unix.Openat(current, components[len(components)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	_ = unix.Close(current)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("open staged evidence without following links: %w", err)
	}
	return captureArtifactDescriptor(fd, relative)
}

func captureArtifactDescriptor(fd int, name string) (capturedArtifactSource, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return capturedArtifactSource{}, errors.New("open staged evidence descriptor")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return capturedArtifactSource{}, errors.New("staged evidence must be a regular file")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Nlink != 1 {
		return capturedArtifactSource{}, errors.New("staged evidence must have exactly one link")
	}
	if stat.Size > maxStagedEvidenceBytes {
		return capturedArtifactSource{}, fmt.Errorf("%w: declared size %d exceeds maximum %d bytes", ErrStagedEvidenceTooLarge, stat.Size, maxStagedEvidenceBytes)
	}
	content, digest, err := readStagedEvidence(file)
	if err != nil {
		return capturedArtifactSource{}, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return capturedArtifactSource{}, errors.New("staged evidence changed while being captured")
	}
	return capturedArtifactSource{Content: content, SHA256: digest, CapturedAt: time.Now().UTC()}, nil
}
