// Package renderer builds the corrected-resume DOCX from documented facts.
// Only reorganized facts are emitted; nothing the evaluation did not find
// enters the document. XML is written by hand with control-character
// scrubbing so one stray codepoint cannot produce a file Word rejects.
package renderer

import (
	"archive/zip"
	"bytes"
	"strings"
)

// MaxRenderedTextCharacters bounds model-extracted strings per field.
const MaxRenderedTextCharacters = 2000

// RenderResumeDocx assembles contact, skills, experience, education,
// certifications, and improvement notes.
func RenderResumeDocx(facts map[string]any, suggestions []map[string]any) ([]byte, error) {
	var body strings.Builder
	contact, _ := facts["contact"].(map[string]any)
	name := textOf(contact["name"])
	if name == "" {
		name = "Resume"
	}
	writeHeading(&body, name, 0)
	var contactParts []string
	for _, field := range []string{"email", "phone", "location"} {
		if text := textOf(contact[field]); text != "" {
			contactParts = append(contactParts, text)
		}
	}
	if len(contactParts) > 0 {
		writeParagraph(&body, escapeXML(strings.Join(contactParts, " · ")), "")
	}
	if skills := entriesOf(facts["skills"]); len(skills) > 0 {
		writeHeading(&body, "Skills", 1)
		grouped := make(map[string][]string)
		var categories []string
		for _, skill := range skills {
			canonical := textOf(skill["canonicalName"])
			if canonical == "" {
				continue
			}
			category := textOf(skill["category"])
			if category == "" {
				category = "Other"
			}
			if _, ok := grouped[category]; !ok {
				categories = append(categories, category)
			}
			grouped[category] = append(grouped[category], canonical)
		}
		sortStrings(categories)
		for _, category := range categories {
			names := grouped[category]
			sortStringsFold(names)
			writeParagraph(&body, escapeXML(category+": "+strings.Join(names, ", ")), "")
		}
	}
	if employment := entriesOf(facts["employment"]); len(employment) > 0 {
		writeHeading(&body, "Experience", 1)
		for _, role := range employment {
			var titleParts []string
			for _, field := range []string{"title", "employer"} {
				if text := textOf(role[field]); text != "" {
					titleParts = append(titleParts, text)
				}
			}
			title := strings.Join(titleParts, " — ")
			start := textOf(role["startDate"])
			end := textOf(role["endDate"])
			var dates string
			switch {
			case role["isCurrent"] == true && start != "":
				dates = start + " to present"
			case start != "" && end != "":
				dates = start + " to " + end
			default:
				dates = start + end
			}
			line := title
			if title != "" && dates != "" {
				line = title + " (" + dates + ")"
			} else if dates != "" {
				line = dates
			} else if line == "" {
				line = "Role"
			}
			writeParagraph(&body, escapeXML(line), "")
		}
	}
	if education := entriesOf(facts["education"]); len(education) > 0 {
		writeHeading(&body, "Education", 1)
		for _, entry := range education {
			var parts []string
			for _, field := range []string{"degree", "fieldOfStudy", "institution"} {
				if text := textOf(entry[field]); text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				writeParagraph(&body, escapeXML(strings.Join(parts, ", ")), "")
			}
		}
	}
	if certifications := entriesOf(facts["certifications"]); len(certifications) > 0 {
		writeHeading(&body, "Certifications", 1)
		for _, entry := range certifications {
			var parts []string
			for _, field := range []string{"name", "issuer"} {
				if text := textOf(entry[field]); text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				writeParagraph(&body, escapeXML(strings.Join(parts, " — ")), "")
			}
		}
	}
	if len(suggestions) > 0 {
		writeHeading(&body, "Improvement notes", 1)
		for _, suggestion := range suggestions {
			title := textOf(suggestion["title"])
			detail := textOf(suggestion["detail"])
			line := title
			if title != "" && detail != "" {
				line = title + ": " + detail
			} else if detail != "" {
				line = detail
			}
			if line != "" {
				writeParagraph(&body, escapeXML(line), "ListBullet")
			}
		}
	}
	documentXML := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body.String() + `</w:body></w:document>`
	contentTypes := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
		`</Types>`
	relationships := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
		`</Relationships>`
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, part := range []struct{ name, content string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", relationships},
		{"word/document.xml", documentXML},
	} {
		entry, err := writer.Create(part.name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(part.content)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeHeading(body *strings.Builder, text string, level int) {
	style := "Title"
	if level > 0 {
		style = "Heading" + itoa(level)
	}
	body.WriteString(`<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:t xml:space="preserve">` + escapeXML(text) + `</w:t></w:r></w:p>`)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func writeParagraph(body *strings.Builder, text, style string) {
	if style != "" {
		body.WriteString(`<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`)
		return
	}
	body.WriteString(`<w:p><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`)
}

// textOf scrubs XML 1.0 control characters and bounds field length.
func textOf(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	var cleaned []rune
	for _, r := range text {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 {
			cleaned = append(cleaned, r)
		}
	}
	trimmed := strings.TrimSpace(string(cleaned))
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > MaxRenderedTextCharacters {
		runes = runes[:MaxRenderedTextCharacters]
	}
	return string(runes)
}

func escapeXML(text string) string {
	var out strings.Builder
	for _, r := range text {
		switch r {
		case '&':
			out.WriteString("&amp;")
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		case '"':
			out.WriteString("&quot;")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func entriesOf(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func sortStringsFold(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && strings.ToLower(values[j]) < strings.ToLower(values[j-1]); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
