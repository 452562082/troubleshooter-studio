//go:build !windows

package bughub

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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

func captureArtifactSource(path string) (capturedArtifactSource, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("open artifact source without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return capturedArtifactSource{}, fmt.Errorf("open artifact source descriptor")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("inspect artifact source: %w", err)
	}
	if !before.Mode().IsRegular() {
		return capturedArtifactSource{}, fmt.Errorf("artifact source must be a regular file")
	}
	if before.Size() > maxEvidenceArtifactBytes {
		return capturedArtifactSource{}, fmt.Errorf("%w: declared size %d exceeds maximum %d bytes", ErrEvidenceArtifactTooLarge, before.Size(), maxEvidenceArtifactBytes)
	}
	content, digest, err := readStagedEvidence(file)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("read artifact source: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("reinspect artifact source: %w", err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return capturedArtifactSource{}, fmt.Errorf("artifact source changed while being captured")
	}
	return capturedArtifactSource{Content: content, SHA256: digest, CapturedAt: after.ModTime().UTC()}, nil
}

func captureRegisteredArtifact(path, artifactsRoot, caseID, digest string) (capturedArtifactSource, error) {
	caseComponent := artifactStorageCaseComponent(caseID)
	if !filepath.IsAbs(path) || caseID == "" || digest == "" || filepath.Base(path) != digest || filepath.Base(filepath.Dir(path)) != caseComponent {
		return capturedArtifactSource{}, errors.New("registered artifact path ownership is invalid")
	}
	registeredRoot := filepath.Dir(filepath.Dir(filepath.Clean(path)))
	if strings.TrimSpace(artifactsRoot) != "" {
		expectedRoot, err := filepath.Abs(artifactsRoot)
		if err != nil || filepath.Clean(expectedRoot) != registeredRoot {
			return capturedArtifactSource{}, errors.New("registered artifact does not belong to the configured artifact store")
		}
	}
	volumeRoot := filepath.VolumeName(registeredRoot) + string(filepath.Separator)
	if filepath.Clean(registeredRoot) == filepath.Clean(volumeRoot) {
		return capturedArtifactSource{}, errors.New("registered artifact root must be a dedicated subdirectory")
	}
	rootFD, err := openDirectoryPath(registeredRoot)
	if err != nil {
		return capturedArtifactSource{}, err
	}
	defer unix.Close(rootFD)
	caseFD, err := unix.Openat(rootFD, caseComponent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("open registered artifact case directory without following links: %w", err)
	}
	defer unix.Close(caseFD)
	fd, err := unix.Openat(caseFD, digest, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("open registered artifact without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return capturedArtifactSource{}, errors.New("open registered artifact descriptor")
	}
	defer file.Close()
	beforeInfo, err := file.Stat()
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("inspect registered artifact: %w", err)
	}
	var beforeStat unix.Stat_t
	if err := unix.Fstat(fd, &beforeStat); err != nil {
		return capturedArtifactSource{}, fmt.Errorf("inspect registered artifact descriptor: %w", err)
	}
	if !beforeInfo.Mode().IsRegular() || beforeStat.Nlink != 1 {
		return capturedArtifactSource{}, errors.New("registered artifact must be a regular file with one link")
	}
	if beforeInfo.Size() < 0 || beforeInfo.Size() > maxEvidenceArtifactBytes {
		return capturedArtifactSource{}, fmt.Errorf("%w: declared size %d exceeds maximum %d bytes", ErrEvidenceArtifactTooLarge, beforeInfo.Size(), maxEvidenceArtifactBytes)
	}
	content, actualDigest, err := readStagedEvidence(file)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("read registered artifact: %w", err)
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("reinspect registered artifact: %w", err)
	}
	var afterStat unix.Stat_t
	if err := unix.Fstat(fd, &afterStat); err != nil {
		return capturedArtifactSource{}, fmt.Errorf("reinspect registered artifact descriptor: %w", err)
	}
	if !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Size() != afterInfo.Size() || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) ||
		beforeStat.Dev != afterStat.Dev || beforeStat.Ino != afterStat.Ino || beforeStat.Mode != afterStat.Mode || beforeStat.Nlink != afterStat.Nlink || beforeStat.Size != afterStat.Size {
		return capturedArtifactSource{}, errors.New("registered artifact changed while being read")
	}
	if int64(len(content)) != beforeInfo.Size() || actualDigest != digest {
		return capturedArtifactSource{}, errors.New("registered artifact digest changed or size changed")
	}
	return capturedArtifactSource{Content: content, SHA256: actualDigest, CapturedAt: afterInfo.ModTime().UTC()}, nil
}

type unixArtifactPublication struct {
	rootPath string
	caseID   string
	digest   string
	path     string
	rootFD   int
	caseFD   int
	rootInfo os.FileInfo
	caseInfo os.FileInfo
	destInfo os.FileInfo
	created  bool
}

func publishArtifact(rootPath, caseID, digest string, content []byte) (artifactPublication, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	rootFD, err := openOrCreateDirectoryPath(absRoot)
	if err != nil {
		return nil, err
	}
	caseComponent := artifactStorageCaseComponent(caseID)
	publication := &unixArtifactPublication{rootPath: absRoot, caseID: caseComponent, digest: digest, rootFD: rootFD, caseFD: -1}
	fail := func(err error) (artifactPublication, error) {
		_ = publication.Close()
		return nil, err
	}
	if err := unix.Fchmod(rootFD, 0o700); err != nil {
		return fail(fmt.Errorf("secure artifact root: %w", err))
	}
	publication.rootInfo, err = fileInfoFromFD(rootFD, absRoot)
	if err != nil {
		return fail(err)
	}
	caseFD, err := openOrCreateDirectoryAt(rootFD, caseComponent)
	if err != nil {
		return fail(err)
	}
	publication.caseFD = caseFD
	if err := unix.Fchmod(caseFD, 0o700); err != nil {
		return fail(fmt.Errorf("secure artifact case directory: %w", err))
	}
	publication.caseInfo, err = fileInfoFromFD(caseFD, caseComponent)
	if err != nil {
		return fail(err)
	}
	publication.path = filepath.Join(absRoot, caseComponent, digest)

	if existingFD, openErr := unix.Openat(caseFD, digest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0); openErr == nil {
		defer unix.Close(existingFD)
		info, err := verifyPublishedDescriptor(existingFD, digest, content)
		if err != nil {
			return fail(fmt.Errorf("verify existing artifact destination: %w", err))
		}
		publication.destInfo = info
		return publication, nil
	} else if !errors.Is(openErr, unix.ENOENT) {
		return fail(fmt.Errorf("open existing artifact destination: %w", openErr))
	}

	tempName, tempFD, err := createTemporaryAt(caseFD)
	if err != nil {
		return fail(err)
	}
	tempFile := os.NewFile(uintptr(tempFD), tempName)
	if tempFile == nil {
		_ = unix.Close(tempFD)
		_ = unix.Unlinkat(caseFD, tempName, 0)
		return fail(fmt.Errorf("open artifact temporary descriptor"))
	}
	tempOpen := true
	defer func() {
		if tempOpen {
			_ = tempFile.Close()
		}
		if tempName != "" {
			_ = unix.Unlinkat(caseFD, tempName, 0)
		}
	}()
	if err := tempFile.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure artifact temporary file: %w", err))
	}
	if _, err := tempFile.Write(content); err != nil {
		return fail(fmt.Errorf("write artifact: %w", err))
	}
	if err := tempFile.Sync(); err != nil {
		return fail(fmt.Errorf("sync artifact: %w", err))
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind artifact: %w", err))
	}
	publication.destInfo, err = verifyRegularFile(tempFile, digest, content)
	if err != nil {
		return fail(err)
	}
	if err := unix.Linkat(caseFD, tempName, caseFD, digest, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return fail(fmt.Errorf("publish artifact: %w", err))
		}
		existingFD, openErr := unix.Openat(caseFD, digest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if openErr != nil {
			return fail(fmt.Errorf("open concurrently published artifact: %w", openErr))
		}
		info, verifyErr := verifyPublishedDescriptor(existingFD, digest, content)
		_ = unix.Close(existingFD)
		if verifyErr != nil {
			return fail(fmt.Errorf("verify concurrently published artifact: %w", verifyErr))
		}
		publication.destInfo = info
	} else {
		publication.created = true
		if err := unix.Unlinkat(caseFD, tempName, 0); err != nil {
			return fail(fmt.Errorf("remove artifact temporary link: %w", err))
		}
		tempName = ""
	}
	if err := tempFile.Close(); err != nil {
		return fail(fmt.Errorf("close artifact temporary file: %w", err))
	}
	tempOpen = false
	if tempName != "" {
		if err := unix.Unlinkat(caseFD, tempName, 0); err != nil {
			return fail(fmt.Errorf("remove artifact temporary link: %w", err))
		}
		tempName = ""
	}
	return publication, nil
}

func verifyRegisteredArtifact(path, digest string) error {
	parentFD, err := openDirectoryPath(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open registered artifact: %w", err)
	}
	defer unix.Close(fd)
	_, err = verifyPublishedDescriptor(fd, digest, nil)
	return err
}

func (p *unixArtifactPublication) Path() string  { return p.path }
func (p *unixArtifactPublication) Created() bool { return p.created }

func (p *unixArtifactPublication) Verify() error {
	rootFD, err := openDirectoryPath(p.rootPath)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	rootInfo, err := fileInfoFromFD(rootFD, p.rootPath)
	if err != nil || !os.SameFile(rootInfo, p.rootInfo) {
		return fmt.Errorf("artifact root changed during registration")
	}
	caseFD, err := unix.Openat(rootFD, p.caseID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("reopen artifact case directory: %w", err)
	}
	defer unix.Close(caseFD)
	caseInfo, err := fileInfoFromFD(caseFD, p.caseID)
	if err != nil || !os.SameFile(caseInfo, p.caseInfo) {
		return fmt.Errorf("artifact case directory changed during registration")
	}
	destFD, err := unix.Openat(caseFD, p.digest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("reopen artifact destination: %w", err)
	}
	defer unix.Close(destFD)
	destInfo, err := verifyPublishedDescriptor(destFD, p.digest, nil)
	if err != nil {
		return err
	}
	if !os.SameFile(destInfo, p.destInfo) {
		return fmt.Errorf("artifact destination changed during registration")
	}
	return nil
}

func (p *unixArtifactPublication) Cleanup() error {
	if !p.created || p.caseFD < 0 {
		return nil
	}
	fd, err := unix.Openat(p.caseFD, p.digest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open artifact for cleanup: %w", err)
	}
	info, verifyErr := verifyPublishedDescriptor(fd, p.digest, nil)
	_ = unix.Close(fd)
	if verifyErr != nil || !os.SameFile(info, p.destInfo) {
		return fmt.Errorf("refuse to clean artifact no longer owned by this registration")
	}
	if err := unix.Unlinkat(p.caseFD, p.digest, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("clean uncommitted artifact: %w", err)
	}
	return nil
}

func (p *unixArtifactPublication) Close() error {
	var first error
	if p.caseFD >= 0 {
		first = unix.Close(p.caseFD)
		p.caseFD = -1
	}
	if p.rootFD >= 0 {
		if err := unix.Close(p.rootFD); first == nil {
			first = err
		}
		p.rootFD = -1
	}
	return first
}

func openOrCreateDirectoryPath(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("artifact root must be absolute")
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open filesystem root: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, fmt.Errorf("create artifact root component %q: %w", component, mkdirErr)
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		_ = unix.Close(current)
		if openErr != nil {
			return -1, fmt.Errorf("open artifact root component %q without following links: %w", component, openErr)
		}
		current = next
	}
	return current, nil
}

func openDirectoryPath(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("artifact root must be absolute")
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, fmt.Errorf("open artifact path component %q without following links: %w", component, openErr)
		}
		current = next
	}
	return current, nil
}

func openOrCreateDirectoryAt(parentFD int, name string) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		if mkdirErr := unix.Mkdirat(parentFD, name, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return -1, fmt.Errorf("create artifact case directory: %w", mkdirErr)
		}
		fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open artifact case directory without following links: %w", err)
	}
	return fd, nil
}

func createTemporaryAt(caseFD int) (string, int, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, fmt.Errorf("generate artifact temporary name: %w", err)
		}
		name := ".artifact-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(caseFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil {
			return name, fd, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", -1, fmt.Errorf("create artifact temporary file: %w", err)
		}
	}
	return "", -1, fmt.Errorf("create unique artifact temporary file")
}

func verifyRegularDescriptor(fd int, digest string, content []byte) (os.FileInfo, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("duplicate artifact descriptor: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "artifact")
	if file == nil {
		_ = unix.Close(duplicate)
		return nil, fmt.Errorf("open artifact descriptor")
	}
	defer file.Close()
	return verifyRegularFile(file, digest, content)
}

func verifyPublishedDescriptor(fd int, digest string, content []byte) (os.FileInfo, error) {
	for attempt := 0; attempt < 20; attempt++ {
		info, err := verifyRegularDescriptor(fd, digest, content)
		if err == nil {
			return info, nil
		}
		var stat unix.Stat_t
		if fstatErr := unix.Fstat(fd, &stat); fstatErr != nil || stat.Nlink != 2 {
			return nil, err
		}
		time.Sleep(time.Millisecond)
	}
	return verifyRegularDescriptor(fd, digest, content)
}

func verifyRegularFile(file *os.File, digest string, content []byte) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect artifact destination: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return nil, fmt.Errorf("inspect artifact destination links: %w", err)
	}
	if !info.Mode().IsRegular() || stat.Nlink != 1 {
		return nil, fmt.Errorf("artifact destination must be a regular file with one link")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind artifact destination: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, fmt.Errorf("hash artifact destination: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, fmt.Errorf("artifact destination content conflicts with digest")
	}
	if content != nil {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		actual, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(actual, content) {
			return nil, fmt.Errorf("artifact destination bytes changed during publication")
		}
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("reinspect artifact destination: %w", err)
	}
	if !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("artifact destination changed while being verified")
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("secure artifact destination: %w", err)
	}
	return info, nil
}

func fileInfoFromFD(fd int, name string) (os.FileInfo, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("duplicate directory descriptor %q: %w", name, err)
	}
	file := os.NewFile(uintptr(duplicate), name)
	if file == nil {
		_ = unix.Close(duplicate)
		return nil, fmt.Errorf("open directory descriptor %q", name)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect directory descriptor %q: %w", name, err)
	}
	return info, nil
}
