package bughub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const maxBrowserUploadFiles = 4

var browserUploadMIMETypes = map[string]map[string]struct{}{
	".csv":  {"text/csv": {}, "application/csv": {}, "text/plain": {}, "application/octet-stream": {}},
	".tsv":  {"text/tab-separated-values": {}, "text/plain": {}, "application/octet-stream": {}},
	".txt":  {"text/plain": {}, "application/octet-stream": {}},
	".json": {"application/json": {}, "text/json": {}, "text/plain": {}, "application/octet-stream": {}},
	".xml":  {"application/xml": {}, "text/xml": {}, "text/plain": {}, "application/octet-stream": {}},
	".pdf":  {"application/pdf": {}, "application/octet-stream": {}},
	".xls":  {"application/vnd.ms-excel": {}, "application/octet-stream": {}},
	".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": {}, "application/zip": {}, "application/octet-stream": {}},
	".doc":  {"application/msword": {}, "application/octet-stream": {}},
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {}, "application/zip": {}, "application/octet-stream": {}},
	".ppt":  {"application/vnd.ms-powerpoint": {}, "application/octet-stream": {}},
	".pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation": {}, "application/zip": {}, "application/octet-stream": {}},
	".png":  {"image/png": {}, "application/octet-stream": {}},
	".jpg":  {"image/jpeg": {}, "application/octet-stream": {}},
	".jpeg": {"image/jpeg": {}, "application/octet-stream": {}},
	".gif":  {"image/gif": {}, "application/octet-stream": {}},
	".webp": {"image/webp": {}, "application/octet-stream": {}},
	".mp3":  {"audio/mpeg": {}, "application/octet-stream": {}},
	".wav":  {"audio/wav": {}, "audio/x-wav": {}, "application/octet-stream": {}},
	".m4a":  {"audio/mp4": {}, "audio/x-m4a": {}, "application/octet-stream": {}},
	".mp4":  {"video/mp4": {}, "application/octet-stream": {}},
	".mov":  {"video/quicktime": {}, "application/octet-stream": {}},
	".webm": {"video/webm": {}, "application/octet-stream": {}},
}

// NormalizeBrowserUploadFileName validates a user-visible filename and MIME
// type without accepting paths or executable/script formats.
func NormalizeBrowserUploadFileName(name, mimeType string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 || filepath.Base(name) != name || strings.ContainsAny(name, "\r\n\x00/\\") {
		return "", errors.New("browser upload filename is invalid")
	}
	extension := strings.ToLower(filepath.Ext(name))
	allowed, ok := browserUploadMIMETypes[extension]
	if !ok {
		return "", fmt.Errorf("browser upload file type %q is not supported", extension)
	}
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if mimeType != "" {
		if _, ok := allowed[mimeType]; !ok {
			return "", errors.New("browser upload MIME type does not match the file extension")
		}
	}
	return name, nil
}

func browserUploadMIMETypeForExtension(extension string) string {
	switch strings.ToLower(extension) {
	case ".csv":
		return "text/csv"
	case ".tsv":
		return "text/tab-separated-values"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".pdf":
		return "application/pdf"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	default:
		return "application/octet-stream"
	}
}

func isUserSupplementalBrowserUpload(attachment Attachment) bool {
	return strings.HasPrefix(strings.TrimSpace(attachment.Name), "用户补充测试文件-")
}

func browserAttachmentUploadCandidate(attachment Attachment) bool {
	if strings.TrimSpace(attachment.LocalPath) == "" {
		return false
	}
	if isUserSupplementalBrowserUpload(attachment) {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(attachment.Type))
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.Name)))
	// Historical screenshots are evidence, not implicit business input. A user
	// can still explicitly attach an image as a test file through the Case UI.
	if strings.HasPrefix(kind, "image/") || extension == ".png" || extension == ".jpg" || extension == ".jpeg" || extension == ".gif" || extension == ".webp" {
		return false
	}
	_, err := NormalizeBrowserUploadFileName(attachment.Name, attachment.Type)
	return err == nil
}

func prepareBrowserUploadFiles(bug Bug) ([]BrowserUploadFile, []map[string]string, error) {
	candidates := append([]Attachment(nil), bug.Attachments...)
	sort.SliceStable(candidates, func(left, right int) bool {
		return isUserSupplementalBrowserUpload(candidates[left]) && !isUserSupplementalBrowserUpload(candidates[right])
	})
	files := make([]BrowserUploadFile, 0, maxBrowserUploadFiles)
	manifest := make([]map[string]string, 0, maxBrowserUploadFiles)
	seen := make(map[string]struct{}, maxBrowserUploadFiles)
	for _, attachment := range candidates {
		if len(files) >= maxBrowserUploadFiles || !browserAttachmentUploadCandidate(attachment) {
			continue
		}
		name, err := NormalizeBrowserUploadFileName(attachment.Name, attachment.Type)
		if err != nil {
			continue
		}
		captured, err := captureArtifactSource(strings.TrimSpace(attachment.LocalPath))
		if err != nil || len(captured.Content) == 0 || int64(len(captured.Content)) > maxEvidenceArtifactBytes {
			if isUserSupplementalBrowserUpload(attachment) {
				if err == nil {
					err = errors.New("supplemental browser upload is empty or too large")
				}
				return nil, nil, fmt.Errorf("capture supplemental browser upload %q: %w", name, err)
			}
			continue
		}
		ref := strings.TrimSpace(attachment.ID)
		if ref == "" {
			ref = "file-" + captured.SHA256[:16]
		}
		if err := validateBrowserPlanString("browser upload file_ref", ref, true); err != nil || strings.ContainsAny(ref, `/\\`) {
			continue
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		content := append([]byte(nil), captured.Content...)
		digest := sha256.Sum256(content)
		files = append(files, BrowserUploadFile{
			ID: ref, Name: name, MIMEType: browserUploadMIMETypeForExtension(filepath.Ext(name)),
			Content: content, SHA256: hex.EncodeToString(digest[:]),
		})
		source := "original_bug"
		if isUserSupplementalBrowserUpload(attachment) {
			source = "user_supplemental"
		}
		manifest = append(manifest, map[string]string{
			"id": ref, "name": name, "type": browserUploadMIMETypeForExtension(filepath.Ext(name)), "source": source,
		})
	}
	return files, manifest, nil
}

func validateBrowserPlanUploadBindings(plan BrowserPlan, files []BrowserUploadFile) error {
	available := make(map[string]struct{}, len(files))
	for _, file := range files {
		available[file.ID] = struct{}{}
	}
	for _, action := range plan.Actions {
		if action.Action != "upload_file" {
			continue
		}
		if _, ok := available[action.FileRef]; !ok {
			return fmt.Errorf("browser upload action %q references an unavailable controlled file", action.ID)
		}
	}
	return nil
}

func browserPlanHasUpload(plan BrowserPlan) bool {
	for _, action := range plan.Actions {
		if action.Action == "upload_file" {
			return true
		}
	}
	return false
}

func browserScenarioRequiresFileUpload(request BrowserCoordinatorRequest, observation *BrowserVerificationResult) bool {
	values := []string{request.Bug.Steps}
	if len(request.UserClarifications) > 0 {
		values = append(values, request.UserClarifications[len(request.UserClarifications)-1])
	}
	if strings.TrimSpace(request.Bug.Steps) == "" {
		values = append(values, request.Bug.Title, request.Bug.Description, request.Bug.Actual)
	}
	text := strings.ToLower(strings.Join(values, "\n"))
	for _, marker := range []string{
		"选择文件", "选取文件", "上传文件", "上传媒资", "上传素材", "上传图片", "上传视频", "上传音频",
		"导入文件", "导入表格", "批量导入", "upload file", "choose file", "select file", "import file",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if (strings.Contains(text, "选择") || strings.Contains(text, "选取")) && strings.Contains(text, "文件") {
		return true
	}
	if (strings.Contains(text, "choose") || strings.Contains(text, "select")) && strings.Contains(text, "file") {
		return true
	}
	if observation != nil {
		for _, node := range observation.AccessibilitySummary {
			name := strings.ToLower(strings.TrimSpace(node.Name))
			if strings.Contains(name, "选择文件") || strings.Contains(name, "上传文件") || strings.Contains(name, "choose file") || strings.Contains(name, "upload file") {
				return true
			}
		}
	}
	return false
}

func browserMissingUploadFileResult(request BrowserCoordinatorRequest) (BrowserCoordinatorResult, error) {
	environment, _, err := browserAttemptEnvironmentVersion(request.Attempt, request.Bug, request.Bot)
	if err != nil {
		return BrowserCoordinatorResult{}, err
	}
	expected := strings.TrimSpace(request.Bug.Expected)
	if expected == "" {
		expected = "按当前工单步骤完成文件选择后验证业务结果。"
	}
	validation := ValidationResult{
		VerificationStatus: "insufficient_info",
		Environment:        environment,
		ObservedBehavior:   "当前复现步骤需要选择本地文件，但当前 Case 没有可供浏览器安全上传的受控测试文件。",
		ExpectedBehavior:   expected,
		Evidence:           []ArtifactReference{},
		Gaps:               []string{"请在“补充信息”中上传本次复现使用的测试文件；Studio 只会把该 Case 内的受控文件交给验证浏览器。"},
	}
	if request.Attempt.Phase == PhaseRegression {
		if err := bindRegressionValidationResult(request.Attempt, &validation); err != nil {
			return BrowserCoordinatorResult{}, err
		}
	} else {
		scenarioSHA, err := browserValidationRecipeScenarioSHA256(request)
		if err != nil {
			return BrowserCoordinatorResult{}, err
		}
		validation.ScenarioHash = scenarioSHA
	}
	if err := validateValidationResult(validation); err != nil {
		return BrowserCoordinatorResult{}, err
	}
	encoded, err := jsonMarshalValidationResult(validation)
	if err != nil {
		return BrowserCoordinatorResult{}, err
	}
	return BrowserCoordinatorResult{FinalYAML: encoded}, nil
}

func jsonMarshalValidationResult(result ValidationResult) (string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
