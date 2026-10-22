package documents

import (
	"bytes"
	"path"
	"strings"
	"unicode/utf8"
)

// JobDescriptionExtension includes the text formats accepted only for job descriptions.
func JobDescriptionExtension(mediaType string) string {
	switch mediaType {
	case "text/markdown":
		return ".md"
	case "text/html":
		return ".html"
	default:
		return SupportedResumeTypes[mediaType]
	}
}

func ValidateJobDescription(content []byte, mediaType, filename string) (ValidatedResume, error) {
	extension := strings.ToLower(path.Ext(filename))
	canonical := ""
	switch extension {
	case ".md", ".markdown":
		canonical = "text/markdown"
	case ".html", ".htm":
		canonical = "text/html"
	default:
		return ValidateDocument(content, mediaType, filename)
	}
	if mediaType != canonical && mediaType != "text/plain" && mediaType != "application/octet-stream" && mediaType != "" {
		return ValidatedResume{}, &ErrValidation{Message: "Job description filename does not match its media type"}
	}
	if len(content) == 0 || len(content) > MaxResumeBytes {
		return ValidatedResume{}, &ErrValidation{Message: "Document must be between 1 byte and 20 MB"}
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return ValidatedResume{}, &ErrValidation{Message: "Job description must contain UTF-8 text"}
	}
	return ValidatedResume{MediaType: canonical, Extension: JobDescriptionExtension(canonical)}, nil
}
