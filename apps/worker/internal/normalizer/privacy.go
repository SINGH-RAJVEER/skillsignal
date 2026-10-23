package normalizer

import (
	"regexp"
	"strings"
	"unicode"
)

var phonePattern = regexp.MustCompile(`\+?[0-9][0-9 ()-]{7,}[0-9]`)
var dateRangePattern = regexp.MustCompile(`^(?:19|20)\d{2}(?:[-/]\d{1,2})?\s*-\s*(?:19|20)\d{2}(?:[-/]\d{1,2})?$`)
var urlPattern = regexp.MustCompile(`(?i)(?:https?://|www\.)\S+`)
var identityLabel = regexp.MustCompile(`(?i)^(?:name|full name|email|e-mail|phone|mobile|address|location|date of birth|dob|gender|nationality|marital status|religion|pronouns|photo)\s*:`)

// ScoringBlocks redacts identity segments while preserving role and date evidence.
func ScoringBlocks(blocks []Block) []Block {
	contact := ContactFacts(blocks)
	var out []Block
	for _, block := range blocks {
		var lines []string
		for _, line := range strings.Split(block.Text, "\n") {
			var segments []string
			for _, segment := range strings.Split(line, "|") {
				text := strings.TrimSpace(segment)
				if identityLabel.MatchString(text) {
					continue
				}
				identity := false
				for _, value := range contact {
					if value != nil && *value != "" && strings.EqualFold(text, *value) {
						identity = true
					}
				}
				if identity {
					continue
				}
				text = emailPattern.ReplaceAllString(text, "")
				text = urlPattern.ReplaceAllString(text, "")
				text = phonePattern.ReplaceAllStringFunc(text, func(candidate string) string {
					if dateRangePattern.MatchString(candidate) {
						return candidate
					}
					digits := 0
					for _, char := range candidate {
						if unicode.IsDigit(char) {
							digits++
						}
					}
					if digits >= 10 {
						return ""
					}
					return candidate
				})
				if text = strings.TrimSpace(text); text != "" {
					segments = append(segments, text)
				}
			}
			if len(segments) > 0 {
				lines = append(lines, strings.Join(segments, " | "))
			}
		}
		if text := strings.TrimSpace(strings.Join(lines, "\n")); text != "" {
			out = append(out, Block{ID: block.ID, Text: text})
		}
	}
	return out
}
