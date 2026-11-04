package parser

import (
	"bytes"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// ExtractJobDescription accepts the wider set of job-description formats.
func ExtractJobDescription(content []byte, mediaType string) (ParsedDocument, error) {
	if len(content) > 10<<20 {
		return ParsedDocument{}, &ParseError{Message: "Job description exceeds the size limit"}
	}
	switch mediaType {
	case "text/markdown", "text/x-markdown":
		text, err := DecodeText(content)
		if err != nil {
			return ParsedDocument{}, err
		}
		var lines []string
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimLeft(line, "# \t")
			lines = append(lines, line)
		}
		return ParsedDocumentFor(ParagraphBlocks([]string{strings.Join(lines, "\n")}, "markdown", true), mediaType, 1, 0)
	case "text/html":
		tokenizer := html.NewTokenizer(bytes.NewReader(content))
		var text strings.Builder
		hidden := 0
		for {
			switch tokenizer.Next() {
			case html.ErrorToken:
				if tokenizer.Err() != io.EOF {
					return ParsedDocument{}, &ParseError{Message: "Job description HTML is invalid"}
				}
				return ParsedDocumentFor(ParagraphBlocks([]string{text.String()}, "html", true), mediaType, 1, 0)
			case html.StartTagToken, html.SelfClosingTagToken:
				tag, _ := tokenizer.TagName()
				name := string(tag)
				if hiddenTag(name) {
					hidden++
				}
				if hidden == 0 && blockTag(name) {
					text.WriteString("\n")
				}
			case html.EndTagToken:
				tag, _ := tokenizer.TagName()
				if hiddenTag(string(tag)) && hidden > 0 {
					hidden--
				}
				if hidden == 0 && blockTag(string(tag)) {
					text.WriteString("\n")
				}
			case html.TextToken:
				if hidden == 0 {
					text.Write(tokenizer.Text())
				}
			}
		}
	default:
		return ExtractBlocks(content, mediaType)
	}
}

func hiddenTag(name string) bool {
	return name == "script" || name == "style" || name == "head" || name == "template" || name == "noscript"
}

func blockTag(name string) bool {
	switch name {
	case "p", "div", "li", "br", "ul", "ol", "section", "article", "tr", "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}
