package bughub

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var evidenceMIMETypes = map[string]map[string]struct{}{
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

// NormalizeEvidenceFileName validates a user-visible filename and MIME
// type without accepting paths or executable/script formats.
func NormalizeEvidenceFileName(name, mimeType string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 || filepath.Base(name) != name || strings.ContainsAny(name, "\r\n\x00/\\") {
		return "", errors.New("evidence filename is invalid")
	}
	extension := strings.ToLower(filepath.Ext(name))
	allowed, ok := evidenceMIMETypes[extension]
	if !ok {
		return "", fmt.Errorf("evidence file type %q is not supported", extension)
	}
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if mimeType != "" {
		if _, ok := allowed[mimeType]; !ok {
			return "", errors.New("evidence MIME type does not match the file extension")
		}
	}
	return name, nil
}
