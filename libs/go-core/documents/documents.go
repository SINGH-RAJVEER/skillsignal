// Package documents validates resume uploads and batches with the same
// guards as the Python service: size, media type, filename match,
// signatures, ZIP bombs, symlinks, nesting, and macro payloads.
package documents

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	MaxResumeBytes            = 20 * 1024 * 1024
	MaxBatchFiles             = 500
	MaxBatchUncompressedBytes = 500 * 1024 * 1024
	MaxCompressionRatio       = 100
	MaxZipDepth               = 10
	MaxDocxEntries            = 1000
	MaxDocxUncompressedBytes  = 50 * 1024 * 1024
	MediaTypePDF              = "application/pdf"
	MediaTypeDOCX             = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MediaTypeTXT              = "text/plain"
	MediaTypeZIP              = "application/zip"
	MediaTypeZIPAlt           = "application/x-zip-compressed"
)

// SupportedResumeTypes maps media types to required extensions.
var SupportedResumeTypes = map[string]string{
	MediaTypePDF:  ".pdf",
	MediaTypeDOCX: ".docx",
	MediaTypeTXT:  ".txt",
}

// ErrValidation marks a rejected document or batch.
type ErrValidation struct{ Message string }

func (e *ErrValidation) Error() string { return e.Message }

// ValidatedResume is the accepted document shape.
type ValidatedResume struct {
	MediaType string
	Extension string
}

// BatchEntry is one accepted or rejected batch member.
type BatchEntry struct {
	Name    string
	Content []byte
	Reason  string
}

// ValidateDocument enforces size, type, filename, and signature checks.
func ValidateDocument(content []byte, mediaType, filename string) (ValidatedResume, error) {
	if len(content) == 0 || len(content) > MaxResumeBytes {
		return ValidatedResume{}, &ErrValidation{Message: "Document must be between 1 byte and 20 MB"}
	}
	extension, ok := SupportedResumeTypes[mediaType]
	if !ok {
		return ValidatedResume{}, &ErrValidation{Message: "Document must be a PDF, DOCX, or TXT file"}
	}
	if filename == "" || !strings.EqualFold(path.Ext(filename), extension) {
		return ValidatedResume{}, &ErrValidation{Message: "Document filename does not match its media type"}
	}
	if mediaType == MediaTypePDF && !bytes.HasPrefix(content, []byte("%PDF-")) {
		return ValidatedResume{}, &ErrValidation{Message: "Document content is not a PDF"}
	}
	if mediaType == MediaTypeDOCX {
		if err := ValidateDocx(content); err != nil {
			return ValidatedResume{}, err
		}
	}
	return ValidatedResume{MediaType: mediaType, Extension: extension}, nil
}

// ValidateDocx applies the package-level bomb, encryption, and macro guards.
func ValidateDocx(content []byte) error {
	if !bytes.HasPrefix(content, []byte("PK\x03\x04")) {
		return &ErrValidation{Message: "Document content is not a DOCX file"}
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return &ErrValidation{Message: "Document DOCX file is malformed"}
	}
	if len(archive.File) > MaxDocxEntries {
		return &ErrValidation{Message: "Document DOCX has too many package entries"}
	}
	var total uint64
	for _, entry := range archive.File {
		total += entry.UncompressedSize64
	}
	if total > MaxDocxUncompressedBytes {
		return &ErrValidation{Message: "Document DOCX package is too large"}
	}
	for _, entry := range archive.File {
		if entry.Flags&0x1 != 0 {
			return &ErrValidation{Message: "Encrypted DOCX packages are not supported"}
		}
		if entry.CompressedSize64 > 0 && float64(entry.UncompressedSize64)/float64(entry.CompressedSize64) > MaxCompressionRatio {
			return &ErrValidation{Message: "Document DOCX has a suspicious compression ratio"}
		}
	}
	names := make(map[string]struct{}, len(archive.File))
	for _, entry := range archive.File {
		names[entry.Name] = struct{}{}
	}
	if _, ok := names["[Content_Types].xml"]; !ok {
		return &ErrValidation{Message: "Document content is not a DOCX file"}
	}
	if _, ok := names["word/document.xml"]; !ok {
		return &ErrValidation{Message: "Document content is not a DOCX file"}
	}
	for name := range names {
		if strings.HasSuffix(strings.ToLower(name), "vbaproject.bin") {
			return &ErrValidation{Message: "Macro-enabled Office documents are not supported"}
		}
	}
	return nil
}

var drivePattern = regexp.MustCompile(`^[A-Za-z]:`)

// UnsafePath rejects absolute paths, parent escapes, and drive letters.
func UnsafePath(filename string) bool {
	normalized := strings.ReplaceAll(filename, "\\", "/")
	if path.IsAbs(normalized) || drivePattern.MatchString(filename) {
		return true
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// InspectResumeFiles validates loose uploads so one bad file never fails the batch.
func InspectResumeFiles(files []NamedContent) ([]BatchEntry, error) {
	if len(files) == 0 {
		return nil, &ErrValidation{Message: "Resume batch must contain at least one file"}
	}
	if len(files) > MaxBatchFiles {
		return nil, &ErrValidation{Message: "Resume batch exceeds 500 files"}
	}
	var total int64
	for _, file := range files {
		total += int64(len(file.Content))
	}
	if total > MaxBatchUncompressedBytes {
		return nil, &ErrValidation{Message: "Resume batch exceeds 500 MB"}
	}
	entries := make([]BatchEntry, 0, len(files))
	for _, file := range files {
		reason := FileEntryReason(file.Name, file.Content)
		entry := BatchEntry{Name: file.Name}
		if reason != "" {
			entry.Reason = reason
		} else {
			entry.Content = file.Content
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// NamedContent pairs an upload filename with its bytes.
type NamedContent struct {
	Name    string
	Content []byte
}

// FileEntryReason returns the rejection reason for one loose file, if any.
func FileEntryReason(filename string, content []byte) string {
	if UnsafePath(filename) {
		return "Resume file has an unsafe path"
	}
	if len(strings.Split(strings.ReplaceAll(filename, "\\", "/"), "/")) > MaxZipDepth+1 {
		return "Resume file exceeds the nesting limit"
	}
	mediaType := MediaTypeForName(filename)
	if mediaType == "" {
		return "Document must be a PDF, DOCX, or TXT file"
	}
	if _, err := ValidateDocument(content, mediaType, filename); err != nil {
		var validation *ErrValidation
		if errors.As(err, &validation) {
			return validation.Message
		}
		return "Document is invalid"
	}
	return ""
}

// InspectResumeZip validates each archive entry independently.
func InspectResumeZip(content []byte) ([]BatchEntry, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, &ErrValidation{Message: "Resume ZIP file is malformed"}
	}
	var files []*zip.File
	for _, entry := range archive.File {
		if !entry.FileInfo().IsDir() {
			files = append(files, entry)
		}
	}
	if len(files) > MaxBatchFiles {
		return nil, &ErrValidation{Message: "Resume ZIP exceeds 500 files"}
	}
	var total uint64
	for _, entry := range files {
		total += entry.UncompressedSize64
	}
	if total > MaxBatchUncompressedBytes {
		return nil, &ErrValidation{Message: "Resume ZIP exceeds 500 MB uncompressed"}
	}
	entries := make([]BatchEntry, 0, len(files))
	for _, entry := range files {
		if reason := ZipEntryReason(entry); reason != "" {
			entries = append(entries, BatchEntry{Name: entry.Name, Reason: reason})
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			entries = append(entries, BatchEntry{Name: entry.Name, Reason: "ZIP entry could not be read"})
			continue
		}
		entryContent, err := io.ReadAll(io.LimitReader(reader, MaxBatchUncompressedBytes+1))
		reader.Close()
		if err != nil {
			entries = append(entries, BatchEntry{Name: entry.Name, Reason: "ZIP entry could not be read"})
			continue
		}
		if _, err := ValidateDocument(entryContent, MediaTypeForName(entry.Name), entry.Name); err != nil {
			var validation *ErrValidation
			if errors.As(err, &validation) {
				entries = append(entries, BatchEntry{Name: entry.Name, Reason: validation.Message})
			} else {
				entries = append(entries, BatchEntry{Name: entry.Name, Reason: "Document is invalid"})
			}
			continue
		}
		entries = append(entries, BatchEntry{Name: entry.Name, Content: entryContent})
	}
	return entries, nil
}

// ZipEntryReason returns the rejection reason for one archive entry, if any.
func ZipEntryReason(entry *zip.File) string {
	normalized := strings.ReplaceAll(entry.Name, "\\", "/")
	if UnsafePath(entry.Name) {
		return "ZIP entry has an unsafe path"
	}
	if len(strings.Split(normalized, "/"))-1 > MaxZipDepth {
		return "ZIP entry exceeds the nesting limit"
	}
	// Mirror the Python symlink guard on the raw Unix mode bits, since
	// Go's FileInfo only reports symlinks for Unix-created archives.
	if entry.ExternalAttrs>>16&0o170000 == 0o120000 {
		return "ZIP entry is a symlink"
	}
	if entry.Flags&0x1 != 0 {
		return "Encrypted ZIP entries are not supported"
	}
	if strings.HasSuffix(strings.ToLower(entry.Name), ".zip") {
		return "Nested ZIP archives are not supported"
	}
	if entry.CompressedSize64 == 0 && entry.UncompressedSize64 > 0 {
		return "ZIP entry has a suspicious compression ratio"
	}
	if entry.CompressedSize64 > 0 && float64(entry.UncompressedSize64)/float64(entry.CompressedSize64) > MaxCompressionRatio {
		return "ZIP entry has a suspicious compression ratio"
	}
	return ""
}

// MediaTypeForName maps a filename extension to its media type.
func MediaTypeForName(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".pdf":
		return MediaTypePDF
	case ".docx":
		return MediaTypeDOCX
	case ".txt":
		return MediaTypeTXT
	default:
		return ""
	}
}
