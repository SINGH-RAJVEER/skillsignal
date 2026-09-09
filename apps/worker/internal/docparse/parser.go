// Package parser extracts evidence blocks from resume documents with the
// same guards as the Python implementation: page/size/scan checks, DOCX
// bomb and macro guards, extraction budgets, and quality states.
package parser

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	MaxPages                      = 100
	MaxPDFPageDimensionPoints     = 14400
	MaxPDFDecompressedStreamBytes = 25 * 1024 * 1024
	MaxDocxDocumentXMLBytes       = 25 * 1024 * 1024
	MaxDocxPackageEntries         = 1000
	MaxDocxPackageBytes           = 50 * 1024 * 1024
	MaxDocxCompressionRatio       = 100
	MaxExtractedCharacters        = 250000
	MinReliableNonWhitespaceChars = 40
	WordNamespace                 = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	NonLatinScriptShare           = 0.25
	MinLanguageTokens             = 15
)

// ParseError marks unprocessable document input with a safe message.
type ParseError struct{ Message string }

func (e *ParseError) Error() string { return e.Message }

// Block is one evidence block with source position.
type Block struct {
	ID           string
	Page         int
	ReadingOrder int
	Text         string
	Method       string
	BBox         []float64
}

// Metadata describes the extraction result.
type Metadata struct {
	MediaType                   string
	PageCount                   int
	BlockCount                  int
	CharacterCount              int
	NonWhitespaceCharacterCount int
}

// Quality carries the review state and warnings.
type Quality struct {
	State    string
	Warnings []string
}

// ParsedDocument is the extraction artifact.
type ParsedDocument struct {
	Blocks   []Block
	Metadata Metadata
	Quality  Quality
}

var instructionPattern = regexp.MustCompile(`(?i)\b(?:ignore (?:all |any )?(?:previous|prior) instructions|reveal (?:the )?system prompt|give this resume (?:a )?perfect score)\b`)
var tokenPattern = regexp.MustCompile(`[A-Za-z]{2,}`)
var whitespaceRun = regexp.MustCompile(`\s+`)
var blankLineSplit = regexp.MustCompile(`\n[\t ]*\n+`)

var englishWords = func() map[string]struct{} {
	words := strings.Fields(`the and for with from to of on at by an is are was were be been as it its ` +
		`this that these those or but not have has had will would can could their ` +
		`our your they we you ` +
		`experience senior junior engineer engineering manager management developed ` +
		`developer development built designed led managed operated created improved ` +
		`reduced increased implemented delivered maintained supported education ` +
		`university college degree bachelor master science computer software ` +
		`services service platform systems system team teams project projects ` +
		`skills certified certification location united kingdom states corporation ` +
		`corp inc ltd llc group company consultant analyst specialist coordinator ` +
		`director head lead staff principal architect`)
	set := make(map[string]struct{}, len(words))
	for _, word := range words {
		set[word] = struct{}{}
	}
	return set
}()

// ExtractBlocks dispatches on media type.
func ExtractBlocks(content []byte, mediaType string) (ParsedDocument, error) {
	switch {
	case mediaType == "text/plain":
		text, err := DecodeText(content)
		if err != nil {
			return ParsedDocument{}, err
		}
		return ParsedDocumentFor(ParagraphBlocks([]string{text}, "plain_text", true), mediaType, 1, 0)
	case mediaType == "application/pdf":
		return ExtractPDFBlocks(content)
	case strings.HasSuffix(mediaType, "wordprocessingml.document"):
		paragraphs, err := ExtractDocxParagraphs(content)
		if err != nil {
			return ParsedDocument{}, err
		}
		return ParsedDocumentFor(ParagraphBlocks(paragraphs, "docx_xml", false), mediaType, 1, 0)
	default:
		return ParsedDocument{}, &ParseError{Message: "Unsupported resume document type"}
	}
}

// EvidenceBlock builds one numbered block.
func EvidenceBlock(page, readingOrder int, text, method string, bbox []float64) Block {
	return Block{
		ID:           fmt.Sprintf("p%d-b%d", page, readingOrder),
		Page:         page,
		ReadingOrder: readingOrder,
		Text:         text,
		Method:       method,
		BBox:         bbox,
	}
}

// ParagraphBlocks splits page texts on blank lines into numbered blocks.
func ParagraphBlocks(pages []string, method string, splitParagraphs bool) []Block {
	var blocks []Block
	readingOrder := 1
	for pageNumber, pageText := range pages {
		var paragraphs []string
		if splitParagraphs {
			paragraphs = blankLineSplit.Split(pageText, -1)
		} else {
			paragraphs = []string{pageText}
		}
		for _, paragraph := range paragraphs {
			text := strings.TrimSpace(paragraph)
			if text == "" {
				continue
			}
			blocks = append(blocks, EvidenceBlock(pageNumber+1, readingOrder, text, method, nil))
			readingOrder++
		}
	}
	return blocks
}

// ParsedDocumentFor assembles the artifact with quality checks.
func ParsedDocumentFor(blocks []Block, mediaType string, pageCount, emptyPageCount int) (ParsedDocument, error) {
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		texts = append(texts, block.Text)
	}
	joined := strings.Join(texts, "\n\n")
	if strings.TrimSpace(joined) == "" {
		return ParsedDocument{}, &ParseError{Message: "Resume contains no extractable text"}
	}
	if len([]rune(joined)) > MaxExtractedCharacters {
		return ParsedDocument{}, &ParseError{Message: "Resume contains too much extractable text"}
	}
	if err := RejectNonEnglishText(joined); err != nil {
		return ParsedDocument{}, err
	}
	nonWhitespace := 0
	for _, r := range joined {
		if !unicode.IsSpace(r) {
			nonWhitespace++
		}
	}
	var warnings []string
	if nonWhitespace < MinReliableNonWhitespaceChars {
		warnings = append(warnings, "Document contains very little extractable text")
	}
	if emptyPageCount > 0 {
		label := "page"
		if pageCount != 1 {
			label = "pages"
		}
		warnings = append(warnings, fmt.Sprintf("%d of %d %s contained no extractable text", emptyPageCount, pageCount, label))
	}
	if DuplicateBlockRatio(blocks) >= 0.5 {
		warnings = append(warnings, "Extracted text contains repeated blocks that may be duplicated")
	}
	runes := []rune(joined)
	replacement := 0
	for _, r := range runes {
		if r == utf8.RuneError {
			replacement++
		}
	}
	if len(runes) > 0 && float64(replacement)/float64(len(runes)) >= 0.01 {
		warnings = append(warnings, "Extracted text contains many unreadable characters")
	}
	if instructionPattern.MatchString(joined) {
		warnings = append(warnings, "Document contains instruction-like text that requires review")
	}
	state := "ready"
	if len(warnings) > 0 {
		state = "review_required"
	}
	return ParsedDocument{
		Blocks: blocks,
		Metadata: Metadata{
			MediaType:                   mediaType,
			PageCount:                   pageCount,
			BlockCount:                  len(blocks),
			CharacterCount:              len(runes),
			NonWhitespaceCharacterCount: nonWhitespace,
		},
		Quality: Quality{State: state, Warnings: warnings},
	}, nil
}

// DuplicateBlockRatio is the share of blocks repeating an earlier block.
func DuplicateBlockRatio(blocks []Block) float64 {
	if len(blocks) < 2 {
		return 0
	}
	counts := make(map[string]int, len(blocks))
	for _, block := range blocks {
		key := strings.ToLower(strings.TrimSpace(whitespaceRun.ReplaceAllString(block.Text, " ")))
		counts[key]++
	}
	duplicates := 0
	for _, count := range counts {
		duplicates += count - 1
	}
	return float64(duplicates) / float64(len(blocks))
}

// RejectNonEnglishText applies the script-share and function-word heuristics.
func RejectNonEnglishText(text string) error {
	letters := 0
	nonLatin := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if r > 0x24F {
				nonLatin++
			}
		}
	}
	if letters > 0 && float64(nonLatin)/float64(letters) > NonLatinScriptShare {
		return &ParseError{Message: "Resume text uses an unsupported writing system"}
	}
	tokens := tokenPattern.FindAllString(text, -1)
	if len(tokens) >= MinLanguageTokens {
		for _, token := range tokens {
			if _, ok := englishWords[strings.ToLower(token)]; ok {
				return nil
			}
		}
		return &ParseError{Message: "Resume text does not appear to be in English"}
	}
	return nil
}

// DecodeText handles UTF-16 BOMs and UTF-8 resumes.
func DecodeText(content []byte) (string, error) {
	var text string
	switch {
	case bytes.HasPrefix(content, []byte{0xFF, 0xFE}) || bytes.HasPrefix(content, []byte{0xFE, 0xFF}):
		units := make([]uint16, 0, len(content)/2)
		bigEndian := content[0] == 0xFE
		body := content[2:]
		for i := 0; i+1 < len(body); i += 2 {
			var unit uint16
			if bigEndian {
				unit = uint16(body[i])<<8 | uint16(body[i+1])
			} else {
				unit = uint16(body[i]) | uint16(body[i+1])<<8
			}
			units = append(units, unit)
		}
		text = string(decodeUTF16(units))
	case bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}):
		if !utf8.Valid(content[3:]) {
			return "", &ParseError{Message: "Resume text encoding is not supported"}
		}
		text = string(content[3:])
	default:
		if !utf8.Valid(content) {
			return "", &ParseError{Message: "Resume text encoding is not supported"}
		}
		text = string(content)
	}
	return NormalizeText(text, false)
}

func decodeUTF16(units []uint16) []rune {
	var runes []rune
	for i := 0; i < len(units); i++ {
		unit := units[i]
		if unit >= 0xD800 && unit <= 0xDBFF && i+1 < len(units) {
			low := units[i+1]
			if low >= 0xDC00 && low <= 0xDFFF {
				runes = append(runes, rune(0x10000)+rune(unit-0xD800)<<10+rune(low-0xDC00))
				i++
				continue
			}
		}
		runes = append(runes, rune(unit))
	}
	return runes
}

// NormalizeText applies NFKC, newline folding, and control/budget guards.
func NormalizeText(text string, allowEmpty bool) (string, error) {
	normalized := norm.NFKC.String(text)
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, r := range normalized {
		if r < 32 && r != '\n' && r != '\t' {
			return "", &ParseError{Message: "Resume text contains unsupported control characters"}
		}
	}
	if !allowEmpty && strings.TrimSpace(normalized) == "" {
		return "", &ParseError{Message: "Resume text contains no extractable text"}
	}
	if len([]rune(normalized)) > MaxExtractedCharacters {
		return "", &ParseError{Message: "Resume contains too much extractable text"}
	}
	return normalized, nil
}

// ExtractDocxParagraphs reads word/document.xml with package-level guards.
func ExtractDocxParagraphs(content []byte) ([]string, error) {
	if !bytes.HasPrefix(content, []byte("PK\x03\x04")) {
		return nil, &ParseError{Message: "Resume DOCX could not be parsed"}
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, &ParseError{Message: "Resume DOCX could not be parsed"}
	}
	if len(archive.File) > MaxDocxPackageEntries {
		return nil, &ParseError{Message: "Resume DOCX has too many package entries"}
	}
	var total uint64
	for _, entry := range archive.File {
		total += entry.UncompressedSize64
	}
	if total > MaxDocxPackageBytes {
		return nil, &ParseError{Message: "Resume DOCX package is too large"}
	}
	for _, entry := range archive.File {
		if entry.CompressedSize64 > 0 && float64(entry.UncompressedSize64)/float64(entry.CompressedSize64) > MaxDocxCompressionRatio {
			return nil, &ParseError{Message: "Resume DOCX has a suspicious compression ratio"}
		}
		if entry.Flags&0x1 != 0 {
			return nil, &ParseError{Message: "Encrypted DOCX packages are not supported"}
		}
	}
	names := make(map[string]struct{}, len(archive.File))
	for _, entry := range archive.File {
		names[entry.Name] = struct{}{}
		if strings.HasSuffix(strings.ToLower(entry.Name), "vbaproject.bin") {
			return nil, &ParseError{Message: "Macro-enabled DOCX packages are not supported"}
		}
	}
	var documentXML []byte
	for _, entry := range archive.File {
		if entry.Name != "word/document.xml" {
			continue
		}
		if entry.UncompressedSize64 > MaxDocxDocumentXMLBytes {
			return nil, &ParseError{Message: "Resume DOCX decompresses to too much content"}
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, &ParseError{Message: "Resume DOCX could not be parsed"}
		}
		documentXML, err = readLimited(reader, MaxDocxDocumentXMLBytes+1)
		reader.Close()
		if err != nil {
			return nil, &ParseError{Message: "Resume DOCX could not be parsed"}
		}
	}
	if documentXML == nil {
		return nil, &ParseError{Message: "Resume DOCX could not be parsed"}
	}
	if bytes.Contains(documentXML, []byte("<!DOCTYPE")) || bytes.Contains(documentXML, []byte("<!ENTITY")) {
		return nil, &ParseError{Message: "Resume DOCX contains unsupported markup declarations"}
	}
	paragraphs, err := docxParagraphs(documentXML)
	if err != nil {
		return nil, &ParseError{Message: "Resume DOCX could not be parsed"}
	}
	var kept []string
	for _, paragraph := range paragraphs {
		normalized, err := NormalizeText(paragraph, true)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(normalized) != "" {
			kept = append(kept, normalized)
		}
	}
	if len(kept) == 0 {
		return nil, &ParseError{Message: "Resume DOCX contains no extractable text"}
	}
	return kept, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 32768)
	var total int64
	for {
		n, err := reader.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return nil, fmt.Errorf("limit exceeded")
			}
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.Bytes(), nil
}

// docxParagraphs streams w:p elements, collecting w:t text, tabs, and breaks.
func docxParagraphs(documentXML []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(documentXML))
	decoder.Strict = true
	var paragraphs []string
	var current strings.Builder
	inParagraph := false
	inText := false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Space != WordNamespace {
				continue
			}
			switch {
			case node.Name.Local == "p" && !inParagraph:
				inParagraph = true
				current.Reset()
			case node.Name.Local == "t" && inParagraph:
				inText = true
			case node.Name.Local == "tab" && inParagraph:
				current.WriteString("\t")
			case (node.Name.Local == "br" || node.Name.Local == "cr") && inParagraph:
				current.WriteString("\n")
			}
		case xml.EndElement:
			if node.Name.Space != WordNamespace {
				continue
			}
			switch {
			case node.Name.Local == "t":
				inText = false
			case node.Name.Local == "p" && inParagraph:
				paragraphs = append(paragraphs, current.String())
				inParagraph = false
				inText = false
			}
		case xml.CharData:
			if inParagraph && inText {
				current.Write([]byte(node))
			}
		}
	}
	return paragraphs, nil
}
