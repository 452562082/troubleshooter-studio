package bughub

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var phasePNGSignature = []byte{137, 80, 78, 71, 13, 10, 26, 10}

func createPhaseScreenshotViewAt(root string, content []byte) (string, func() error, error) {
	if !bytes.HasPrefix(content, phasePNGSignature) || int64(len(content)) > maxEvidenceArtifactBytes {
		return "", func() error { return nil }, errors.New("attachment screenshot content is invalid")
	}
	if strings.TrimSpace(root) != "" {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return "", func() error { return nil }, errors.New("attachment attachment workspace path is invalid")
		}
		rootInfo, rootErr := os.Stat(root)
		if rootErr != nil || !rootInfo.IsDir() {
			return "", func() error { return nil }, errors.New("attachment attachment workspace is unavailable")
		}
	}
	directory, err := os.MkdirTemp(root, ".tshoot-browser-attachment-")
	if err != nil {
		return "", func() error { return nil }, err
	}
	cleanupDirectory := func() error { return os.Remove(directory) }
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = cleanupDirectory()
		return "", func() error { return nil }, err
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		_ = cleanupDirectory()
		return "", func() error { return nil }, errors.New("attachment screenshot directory is unsafe")
	}
	path := filepath.Join(directory, "final-screenshot.png")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = cleanupDirectory()
		return "", func() error { return nil }, err
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		_ = cleanupDirectory()
		return "", func() error { return nil }, err
	}
	if err := os.Chmod(path, 0o400); err != nil {
		_ = os.Remove(path)
		_ = cleanupDirectory()
		return "", func() error { return nil }, err
	}
	fileInfo, err := os.Lstat(path)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
		_ = os.Chmod(path, 0o600)
		_ = os.Remove(path)
		_ = cleanupDirectory()
		return "", func() error { return nil }, errors.New("attachment screenshot view is unsafe")
	}
	cleanup := func() error {
		currentDirectory, directoryErr := os.Lstat(directory)
		currentFile, fileErr := os.Lstat(path)
		if directoryErr != nil || fileErr != nil || !os.SameFile(directoryInfo, currentDirectory) || !os.SameFile(fileInfo, currentFile) || currentDirectory.Mode()&os.ModeSymlink != 0 || currentFile.Mode()&os.ModeSymlink != 0 {
			return errors.New("attachment screenshot view identity changed before cleanup")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Remove(directory)
	}
	return path, cleanup, nil
}

var errPhaseAttachmentPathEcho = errors.New("agent output contains an ephemeral attachment path")

func phaseResultContainsAttachmentPath(output string, attachments []PhaseAttachment) bool {
	for _, attachment := range attachments {
		path := filepath.Clean(strings.TrimSpace(attachment.Path))
		if path == "." || path == "" {
			continue
		}
		if strings.Contains(output, path) || strings.Contains(output, filepath.Dir(path)) {
			return true
		}
	}
	return false
}
