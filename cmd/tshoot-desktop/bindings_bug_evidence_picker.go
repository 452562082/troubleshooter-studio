package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type IncidentEvidenceSelection struct {
	Images []IncidentEvidenceImageInput `json:"images"`
	Files  []IncidentEvidenceFileInput  `json:"files"`
}

// SelectIncidentEvidence only reads files explicitly selected in the native
// dialog. No caller-supplied local path is accepted and no artifact is persisted.
func (a *App) SelectIncidentEvidence() (IncidentEvidenceSelection, error) {
	pick := a.workflowPickEvidence
	if pick == nil {
		pick = pickIncidentEvidenceNative
	}
	paths, err := pick(a.getRuntimeContext())
	if err != nil {
		return IncidentEvidenceSelection{}, errors.New("无法打开文件选择窗口，请重试")
	}
	return readSelectedIncidentEvidence(paths)
}

func pickIncidentEvidenceNative(ctx context.Context) ([]string, error) {
	if runtime.GOOS != "darwin" {
		return wailsruntime.OpenMultipleFilesDialog(ctx, wailsruntime.OpenDialogOptions{Title: "选择截图或证据文件"})
	}
	// Use the same macOS 26 workaround as OpenYAML. NUL delimiters preserve
	// spaces and Unicode paths without invoking a shell or parsing quoted paths.
	output, err := osaChoose(`set selectedFiles to choose file with prompt "选择截图或证据文件（截图和其他文件各最多 4 个，单个不超过 16 MB）" with multiple selections allowed
set selectedPaths to ""
repeat with selectedFile in selectedFiles
set selectedPaths to selectedPaths & (POSIX path of selectedFile) & (ASCII character 0)
end repeat
return selectedPaths`)
	if err != nil || output == "" {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(output, "\x00"), "\x00"), nil
}

func readSelectedIncidentEvidence(paths []string) (IncidentEvidenceSelection, error) {
	result := IncidentEvidenceSelection{Images: []IncidentEvidenceImageInput{}, Files: []IncidentEvidenceFileInput{}}
	if len(paths) > maxIncidentEvidenceImages+maxIncidentEvidenceFiles {
		return result, errors.New("截图和其他文件各最多添加 4 个")
	}
	for _, path := range paths {
		name, err := bughub.NormalizeEvidenceFileName(filepath.Base(path), "")
		if err != nil {
			return IncidentEvidenceSelection{}, errors.New("所选附件的文件名或格式不受支持，请选择图片、文档、表格或音视频文件")
		}
		mimeType := "application/octet-stream"
		switch strings.ToLower(filepath.Ext(name)) {
		case ".png":
			mimeType = "image/png"
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		}
		isImage := strings.HasPrefix(mimeType, "image/")
		if (isImage && len(result.Images) >= maxIncidentEvidenceImages) || (!isImage && len(result.Files) >= maxIncidentEvidenceFiles) {
			return IncidentEvidenceSelection{}, errors.New("PNG/JPEG 截图和其他文件各最多添加 4 个")
		}
		data, err := readIncidentEvidenceFile(path)
		if err != nil {
			return IncidentEvidenceSelection{}, fmt.Errorf("读取附件 %q：%w", name, err)
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		if isImage {
			result.Images = append(result.Images, IncidentEvidenceImageInput{Name: name, MIMEType: mimeType, Base64Data: encoded})
		} else {
			result.Files = append(result.Files, IncidentEvidenceFileInput{Name: name, MIMEType: mimeType, Base64Data: encoded})
		}
	}
	return result, nil
}

func readIncidentEvidenceFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("请选择可读取的普通文件")
	}
	if info.Size() < 1 || info.Size() > maxIncidentEvidenceFileBytes {
		return nil, errors.New("文件不能为空，且单个不超过 16 MB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("无法读取文件，请检查访问权限")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxIncidentEvidenceFileBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxIncidentEvidenceFileBytes {
		return nil, errors.New("文件读取失败或大小超出 16 MB")
	}
	return data, nil
}
