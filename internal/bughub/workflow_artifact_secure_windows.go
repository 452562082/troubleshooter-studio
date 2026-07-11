//go:build windows

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

	"golang.org/x/sys/windows"
)

func captureArtifactSource(path string) (capturedArtifactSource, error) {
	file, err := openWindowsNoFollow(path, windows.GENERIC_READ, windows.FILE_SHARE_READ)
	if err != nil {
		return capturedArtifactSource{}, fmt.Errorf("open artifact source without following links: %w", err)
	}
	defer file.Close()
	before, links, err := windowsRegularInfo(file)
	if err != nil {
		return capturedArtifactSource{}, err
	}
	hash := sha256.New()
	var content bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(&content, hash), file); err != nil {
		return capturedArtifactSource{}, fmt.Errorf("read artifact source: %w", err)
	}
	after, afterLinks, err := windowsRegularInfo(file)
	if err != nil {
		return capturedArtifactSource{}, err
	}
	if links != afterLinks || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return capturedArtifactSource{}, fmt.Errorf("artifact source changed while being captured")
	}
	return capturedArtifactSource{Content: content.Bytes(), SHA256: hex.EncodeToString(hash.Sum(nil)), CapturedAt: after.ModTime().UTC()}, nil
}

type windowsArtifactPublication struct {
	rootPath string
	casePath string
	path     string
	digest   string
	root     *os.File
	caseDir  *os.File
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
	if err := createWindowsDirectoryPath(absRoot); err != nil {
		return nil, err
	}
	root, err := openWindowsDirectoryNoFollow(absRoot)
	if err != nil {
		return nil, err
	}
	publication := &windowsArtifactPublication{rootPath: absRoot, casePath: filepath.Join(absRoot, caseID), path: filepath.Join(absRoot, caseID, digest), digest: digest, root: root}
	fail := func(err error) (artifactPublication, error) {
		_ = publication.Close()
		return nil, err
	}
	publication.rootInfo, err = root.Stat()
	if err != nil {
		return fail(err)
	}
	if err := os.Chmod(absRoot, 0o700); err != nil {
		return fail(fmt.Errorf("secure artifact root: %w", err))
	}
	if err := os.Mkdir(publication.casePath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fail(fmt.Errorf("create artifact case directory: %w", err))
	}
	caseDir, err := openWindowsDirectoryNoFollow(publication.casePath)
	if err != nil {
		return fail(err)
	}
	publication.caseDir = caseDir
	publication.caseInfo, err = caseDir.Stat()
	if err != nil {
		return fail(err)
	}
	if err := os.Chmod(publication.casePath, 0o700); err != nil {
		return fail(fmt.Errorf("secure artifact case directory: %w", err))
	}
	if existing, openErr := openWindowsNoFollow(publication.path, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ); openErr == nil {
		defer existing.Close()
		info, err := verifyWindowsArtifact(existing, digest, content)
		if err != nil {
			return fail(err)
		}
		publication.destInfo = info
		return publication, nil
	} else if !errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(openErr, windows.ERROR_PATH_NOT_FOUND) {
		return fail(fmt.Errorf("open existing artifact destination: %w", openErr))
	}
	tempPath, tempFile, err := createWindowsTemporary(publication.casePath)
	if err != nil {
		return fail(err)
	}
	tempOpen := true
	defer func() {
		if tempOpen {
			_ = tempFile.Close()
		}
		_ = os.Remove(tempPath)
	}()
	if _, err := tempFile.Write(content); err != nil {
		return fail(fmt.Errorf("write artifact: %w", err))
	}
	if err := tempFile.Sync(); err != nil {
		return fail(fmt.Errorf("sync artifact: %w", err))
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	publication.destInfo, err = verifyWindowsArtifact(tempFile, digest, content)
	if err != nil {
		return fail(err)
	}
	if err := tempFile.Close(); err != nil {
		return fail(fmt.Errorf("close artifact temporary file: %w", err))
	}
	tempOpen = false
	if err := os.Link(tempPath, publication.path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fail(fmt.Errorf("publish artifact: %w", err))
		}
		existing, openErr := openWindowsNoFollow(publication.path, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ)
		if openErr != nil {
			return fail(openErr)
		}
		publication.destInfo, err = verifyWindowsArtifact(existing, digest, content)
		_ = existing.Close()
		if err != nil {
			return fail(err)
		}
	} else {
		publication.created = true
		if err := os.Remove(tempPath); err != nil {
			return fail(fmt.Errorf("remove artifact temporary link: %w", err))
		}
		tempPath = ""
	}
	return publication, nil
}

func verifyRegisteredArtifact(path, digest string) error {
	if err := verifyWindowsPathNoLinks(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := openWindowsNoFollow(path, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ)
	if err != nil {
		return fmt.Errorf("open registered artifact: %w", err)
	}
	defer file.Close()
	_, err = verifyWindowsArtifact(file, digest, nil)
	return err
}

func (p *windowsArtifactPublication) Path() string  { return p.path }
func (p *windowsArtifactPublication) Created() bool { return p.created }

func (p *windowsArtifactPublication) Verify() error {
	if err := verifyWindowsPathNoLinks(p.casePath); err != nil {
		return err
	}
	rootInfo, err := os.Stat(p.rootPath)
	if err != nil || !os.SameFile(rootInfo, p.rootInfo) {
		return fmt.Errorf("artifact root changed during registration")
	}
	caseInfo, err := os.Stat(p.casePath)
	if err != nil || !os.SameFile(caseInfo, p.caseInfo) {
		return fmt.Errorf("artifact case directory changed during registration")
	}
	file, err := openWindowsNoFollow(p.path, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ)
	if err != nil {
		return err
	}
	defer file.Close()
	destInfo, err := verifyWindowsArtifact(file, p.digest, nil)
	if err != nil || !os.SameFile(destInfo, p.destInfo) {
		return fmt.Errorf("artifact destination changed during registration")
	}
	return nil
}

func (p *windowsArtifactPublication) Cleanup() error {
	if !p.created {
		return nil
	}
	file, err := openWindowsNoFollow(p.path, windows.GENERIC_READ, windows.FILE_SHARE_READ)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	info, links, statErr := windowsRegularInfo(file)
	_ = file.Close()
	if statErr != nil || links != 1 || !os.SameFile(info, p.destInfo) {
		return fmt.Errorf("refuse to clean artifact no longer owned by this registration")
	}
	return os.Remove(p.path)
}

func (p *windowsArtifactPublication) Close() error {
	var first error
	if p.caseDir != nil {
		first = p.caseDir.Close()
		p.caseDir = nil
	}
	if p.root != nil {
		if err := p.root.Close(); first == nil {
			first = err
		}
		p.root = nil
	}
	return first
}

func openWindowsNoFollow(path string, access, share uint32) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open Windows file handle")
	}
	return file, nil
}

func openWindowsDirectoryNoFollow(path string) (*os.File, error) {
	if err := verifyWindowsPathNoLinks(path); err != nil {
		return nil, err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open Windows directory handle")
	}
	return file, nil
}

func createWindowsDirectoryPath(path string) error {
	volume := filepath.VolumeName(path)
	current := volume + string(os.PathSeparator)
	remainder := strings.TrimPrefix(filepath.Clean(path), current)
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create artifact root component: %w", err)
		}
		if err := verifyWindowsEntryNoLink(current); err != nil {
			return err
		}
	}
	return nil
}

func verifyWindowsPathNoLinks(path string) error {
	volume := filepath.VolumeName(path)
	current := volume + string(os.PathSeparator)
	remainder := strings.TrimPrefix(filepath.Clean(path), current)
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		if err := verifyWindowsEntryNoLink(current); err != nil {
			return err
		}
	}
	return nil
}

func verifyWindowsEntryNoLink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("artifact path contains link or reparse point %q", path)
	}
	return nil
}

func windowsRegularInfo(file *os.File) (os.FileInfo, uint32, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &handleInfo); err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, 0, fmt.Errorf("artifact must be a non-reparse regular file")
	}
	return info, handleInfo.NumberOfLinks, nil
}

func verifyWindowsArtifact(file *os.File, digest string, content []byte) (os.FileInfo, error) {
	info, links, err := windowsRegularInfo(file)
	if err != nil {
		return nil, err
	}
	if links != 1 {
		return nil, fmt.Errorf("artifact destination must have one link")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := sha256.New()
	actual, err := io.ReadAll(io.TeeReader(file, hash))
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest || content != nil && !bytes.Equal(actual, content) {
		return nil, fmt.Errorf("artifact destination content conflicts with digest")
	}
	after, afterLinks, err := windowsRegularInfo(file)
	if err != nil || afterLinks != links || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("artifact destination changed while being verified")
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, err
	}
	return info, nil
}

func createWindowsTemporary(directory string) (string, *os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		path := filepath.Join(directory, ".artifact-"+hex.EncodeToString(random[:]))
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return path, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("create unique artifact temporary file")
}
