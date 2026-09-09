// PDF resume extraction with layout-aware block recovery.
//
// This is a compact content-stream engine covering the subset the product
// needs: FlateDecode streams, WinAnsi/PDFDoc and UTF-16BE show strings,
// and absolute text-matrix positions for the line/column/block heuristics.
// Full font-subset decoding is out of scope; like the Python reader this
// treats unmarked literals as latin-1, which matches digital resumes.
package parser

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	pdfLineTolerancePoints = 3.0
	pdfClusterGapPoints    = 100.0
	pdfBlockMergeFactor    = 1.8
	pdfCharWidthFactor     = 0.55
)

// Fragment is positioned show-text from one text-showing operator.
type Fragment struct {
	Text string
	X    float64
	Y    float64
	Size float64
}

var (
	objectPattern  = regexp.MustCompile(`(?s)(\d+)\s+(\d+)\s+obj\b(.*?)endobj`)
	refPattern     = regexp.MustCompile(`(\d+)\s+(\d+)\s+R`)
	nameType       = regexp.MustCompile(`/Type\s*/(\w+)`)
	pagesRef       = regexp.MustCompile(`/Pages\s+(\d+)\s+(\d+)\s+R`)
	kidsArray      = regexp.MustCompile(`/Kids\s*\[(.*?)\]`)
	mediaBoxArray  = regexp.MustCompile(`/MediaBox\s*\[([^\]]*)\]`)
	contentsValue  = regexp.MustCompile(`/Contents\s*(\[.*?\]|\d+\s+\d+\s+R)`)
	encryptPattern = regexp.MustCompile(`/Encrypt\s`)
	filterPattern  = regexp.MustCompile(`/Filter\s*(/[A-Za-z0-9]+|\[.*?\])`)
)

type pdfObject struct {
	num  string
	body string
}

// ExtractPDFBlocks parses pages and recovers reading-order blocks.
func ExtractPDFBlocks(content []byte) (ParsedDocument, error) {
	if !bytes.HasPrefix(content, []byte("%PDF-")) {
		return ParsedDocument{}, &ParseError{Message: "Resume PDF could not be parsed"}
	}
	if encryptPattern.Match(content) {
		return ParsedDocument{}, &ParseError{Message: "Encrypted resume PDFs are not supported"}
	}
	objects := parseObjects(string(content))
	pages, err := pageDicts(objects)
	if err != nil {
		return ParsedDocument{}, err
	}
	if len(pages) == 0 {
		return ParsedDocument{}, &ParseError{Message: "Resume PDF could not be parsed"}
	}
	if len(pages) > MaxPages {
		return ParsedDocument{}, &ParseError{Message: "Resume PDF exceeds 100 pages"}
	}
	for _, page := range pages {
		width := page.box[2] - page.box[0]
		height := page.box[3] - page.box[1]
		if width <= 0 || height <= 0 || width > MaxPDFPageDimensionPoints || height > MaxPDFPageDimensionPoints {
			return ParsedDocument{}, &ParseError{Message: "Resume PDF has unsupported page dimensions"}
		}
	}
	var blocks []Block
	emptyPages := 0
	for pageNumber, page := range pages {
		streams, err := pageStreams(objects, page)
		if err != nil {
			return ParsedDocument{}, &ParseError{Message: "Resume PDF text could not be extracted"}
		}
		var fragments []Fragment
		var fallback []string
		for _, stream := range streams {
			decoded, err := decodeStream(objects, stream)
			if err != nil {
				continue
			}
			pageFragments, shown := runContentStream(decoded)
			fragments = append(fragments, pageFragments...)
			fallback = append(fallback, shown...)
		}
		fallbackText := ""
		if len(fragments) == 0 {
			fallbackText = strings.TrimSpace(strings.Join(fallback, " "))
		}
		if len(fragments) == 0 && fallbackText == "" {
			emptyPages++
			continue
		}
		pageBlocks := pdfPageBlocks(fragments, fallbackText, pageNumber+1, len(blocks))
		blocks = append(blocks, pageBlocks...)
		if len(pageBlocks) == 0 {
			emptyPages++
		}
	}
	if len(blocks) == 0 {
		return ParsedDocument{}, &ParseError{Message: "Scanned or image-only resume PDFs are not supported"}
	}
	return ParsedDocumentFor(blocks, "application/pdf", len(pages), emptyPages)
}

type pageDict struct {
	box      [4]float64
	contents []string
}

func parseObjects(text string) map[string]pdfObject {
	objects := make(map[string]pdfObject)
	for _, match := range objectPattern.FindAllStringSubmatch(text, -1) {
		objects[match[1]+" "+match[2]] = pdfObject{num: match[1], body: match[3]}
	}
	return objects
}

func pageDicts(objects map[string]pdfObject) ([]pageDict, error) {
	var catalog string
	for _, object := range objects {
		if typeMatch := nameType.FindStringSubmatch(object.body); typeMatch != nil && typeMatch[1] == "Catalog" {
			if ref := pagesRef.FindStringSubmatch(object.body); ref != nil {
				catalog = ref[1] + " " + ref[2]
				break
			}
		}
	}
	if catalog == "" {
		return nil, fmt.Errorf("no catalog")
	}
	root, ok := objects[catalog]
	if !ok {
		return nil, fmt.Errorf("no pages root")
	}
	return walkPages(objects, root)
}

func walkPages(objects map[string]pdfObject, node pdfObject) ([]pageDict, error) {
	typeMatch := nameType.FindStringSubmatch(node.body)
	if typeMatch != nil && typeMatch[1] == "Page" {
		box, err := parseMediaBox(node.body)
		if err != nil {
			return nil, err
		}
		return []pageDict{{box: box, contents: parseContents(node.body)}}, nil
	}
	kids := kidsArray.FindStringSubmatch(node.body)
	if kids == nil {
		return nil, fmt.Errorf("pages node without kids")
	}
	var pages []pageDict
	for _, ref := range refPattern.FindAllStringSubmatch(kids[1], -1) {
		child, ok := objects[ref[1]+" "+ref[2]]
		if !ok {
			continue
		}
		childPages, err := walkPages(objects, child)
		if err != nil {
			continue
		}
		pages = append(pages, childPages...)
	}
	return pages, nil
}

func parseMediaBox(body string) ([4]float64, error) {
	var box [4]float64
	match := mediaBoxArray.FindStringSubmatch(body)
	if match == nil {
		return box, fmt.Errorf("no mediabox")
	}
	fields := strings.Fields(match[1])
	if len(fields) < 4 {
		return box, fmt.Errorf("short mediabox")
	}
	for i := 0; i < 4; i++ {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return box, err
		}
		box[i] = value
	}
	return box, nil
}

func parseContents(body string) []string {
	match := contentsValue.FindStringSubmatch(body)
	if match == nil {
		return nil
	}
	value := strings.TrimSpace(match[1])
	if strings.HasPrefix(value, "[") {
		var refs []string
		for _, ref := range refPattern.FindAllStringSubmatch(value, -1) {
			refs = append(refs, ref[1]+" "+ref[2])
		}
		return refs
	}
	if ref := refPattern.FindStringSubmatch(value); ref != nil {
		return []string{ref[1] + " " + ref[2]}
	}
	return nil
}

type streamRef struct {
	objectKey string
}

func pageStreams(objects map[string]pdfObject, page pageDict) ([]streamRef, error) {
	var streams []streamRef
	for _, ref := range page.contents {
		if _, ok := objects[ref]; ok {
			streams = append(streams, streamRef{objectKey: ref})
		}
	}
	return streams, nil
}

// decodeStream extracts and inflates one stream object body.
func decodeStream(objects map[string]pdfObject, ref streamRef) ([]byte, error) {
	object := objects[ref.objectKey]
	body := object.body
	index := strings.Index(body, "stream")
	if index < 0 {
		return nil, fmt.Errorf("no stream")
	}
	rest := body[index+len("stream"):]
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.LastIndex(rest, "endstream")
	if end < 0 {
		return nil, fmt.Errorf("unterminated stream")
	}
	raw := []byte(rest[:end])
	header := body[:index]
	if filter := filterPattern.FindStringSubmatch(header); filter != nil {
		if strings.Contains(filter[1], "FlateDecode") {
			return inflateCapped(raw)
		}
		if strings.Contains(filter[1], "DCTDecode") || strings.Contains(filter[1], "JPXDecode") || strings.Contains(filter[1], "CCITTFaxDecode") {
			return nil, fmt.Errorf("image stream")
		}
	}
	return raw, nil
}

func inflateCapped(raw []byte) ([]byte, error) {
	try := func(reader io.Reader) ([]byte, error) {
		limited := io.LimitReader(reader, MaxPDFDecompressedStreamBytes+1)
		out, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if len(out) > MaxPDFDecompressedStreamBytes {
			return nil, fmt.Errorf("stream too large")
		}
		return out, nil
	}
	if reader, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
		out, err := try(reader)
		reader.Close()
		if err == nil {
			return out, nil
		}
	}
	reader := flate.NewReader(bytes.NewReader(raw))
	defer reader.Close()
	return try(reader)
}

// --- Content stream interpreter ---

type pdfValue struct {
	kind   string // "num", "str", "name", "array"
	number float64
	bytes  []byte
	array  []pdfValue
}

func runContentStream(data []byte) ([]Fragment, []string) {
	tokens := tokenizeContent(data)
	var fragments []Fragment
	var shown []string
	fontSize := 12.0
	leading := 0.0
	tm := [6]float64{1, 0, 0, 1, 0, 0}
	var operands []pdfValue

	flush := func(operator string) {
		switch operator {
		case "Tf":
			if len(operands) >= 2 {
				if size, ok := operandNumber(operands[len(operands)-1]); ok && size > 0 {
					fontSize = size
				}
			}
		case "Tm":
			if len(operands) >= 6 {
				for i := 0; i < 6; i++ {
					if number, ok := operandNumber(operands[len(operands)-6+i]); ok {
						tm[i] = number
					}
				}
			}
		case "Td":
			if len(operands) >= 2 {
				if tx, ok := operandNumber(operands[len(operands)-2]); ok {
					tm[4] += tx
				}
				if ty, ok := operandNumber(operands[len(operands)-1]); ok {
					tm[5] += ty
				}
			}
		case "TD":
			if len(operands) >= 2 {
				if tx, ok := operandNumber(operands[len(operands)-2]); ok {
					tm[4] += -tx
				}
				if ty, ok := operandNumber(operands[len(operands)-1]); ok {
					tm[5] += ty
					leading = -ty
				}
			}
		case "TL":
			if len(operands) >= 1 {
				if value, ok := operandNumber(operands[len(operands)-1]); ok {
					leading = value
				}
			}
		case "T*":
			tm[5] -= leading
		case "BT":
			tm = [6]float64{1, 0, 0, 1, 0, 0}
		case "Tj", "'":
			if len(operands) >= 1 {
				if text := showBytes(operands[len(operands)-1]); text != "" {
					fragments = append(fragments, Fragment{Text: strings.TrimSpace(text), X: tm[4], Y: tm[5], Size: fontSize})
					shown = append(shown, text)
					tm[4] += fragmentWidth(text, fontSize)
				}
			}
		case "\"":
			if len(operands) >= 3 {
				if text := showBytes(operands[len(operands)-1]); text != "" {
					fragments = append(fragments, Fragment{Text: strings.TrimSpace(text), X: tm[4], Y: tm[5], Size: fontSize})
					shown = append(shown, text)
					tm[4] += fragmentWidth(text, fontSize)
				}
			}
		case "TJ":
			if len(operands) >= 1 && operands[len(operands)-1].kind == "array" {
				var builder strings.Builder
				for _, item := range operands[len(operands)-1].array {
					builder.WriteString(showBytes(item))
				}
				if text := builder.String(); text != "" {
					fragments = append(fragments, Fragment{Text: strings.TrimSpace(text), X: tm[4], Y: tm[5], Size: fontSize})
					shown = append(shown, text)
					tm[4] += fragmentWidth(text, fontSize)
				}
			}
		}
		operands = operands[:0]
	}

	i := 0
	for i < len(tokens) {
		token := tokens[i]
		i++
		switch token.kind {
		case "op":
			flush(token.text)
		case "array":
			operands = append(operands, token.value)
		default:
			operands = append(operands, token.value)
		}
	}
	var kept []Fragment
	for _, fragment := range fragments {
		if strings.TrimSpace(fragment.Text) != "" {
			kept = append(kept, fragment)
		}
	}
	return kept, shown
}

func operandNumber(value pdfValue) (float64, bool) {
	if value.kind == "num" {
		return value.number, true
	}
	return 0, false
}

func showBytes(value pdfValue) string {
	if value.kind != "str" {
		return ""
	}
	return decodePDFBytes(value.bytes)
}

// decodePDFBytes handles UTF-16BE BOM strings and PDFDoc/latin-1 bytes.
func decodePDFBytes(value []byte) string {
	if len(value) >= 2 && value[0] == 0xFE && value[1] == 0xFF {
		var runes []rune
		body := value[2:]
		for i := 0; i+1 < len(body); i += 2 {
			runes = append(runes, rune(body[i])<<8|rune(body[i+1]))
		}
		return string(runes)
	}
	runes := make([]rune, 0, len(value))
	for _, b := range value {
		runes = append(runes, rune(b))
	}
	return string(runes)
}

type contentToken struct {
	kind  string // "num", "str", "name", "array", "op"
	text  string
	value pdfValue
}

func tokenizeContent(data []byte) []contentToken {
	var tokens []contentToken
	i := 0
	for i < len(data) {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0:
			i++
		case c == '%':
			for i < len(data) && data[i] != '\r' && data[i] != '\n' {
				i++
			}
		case c == '(':
			raw, next := scanLiteral(data, i)
			i = next
			tokens = append(tokens, contentToken{kind: "str", value: pdfValue{kind: "str", bytes: raw}})
		case c == '<' && i+1 < len(data) && data[i+1] == '<':
			i += 2
		case c == '>' && i+1 < len(data) && data[i+1] == '>':
			i += 2
		case c == '<':
			raw, next := scanHex(data, i)
			i = next
			tokens = append(tokens, contentToken{kind: "str", value: pdfValue{kind: "str", bytes: raw}})
		case c == '[':
			array, next := scanArray(data, i)
			i = next
			tokens = append(tokens, contentToken{kind: "array", value: array})
		case c == '/':
			j := i + 1
			for j < len(data) && !isContentDelim(data[j]) {
				j++
			}
			tokens = append(tokens, contentToken{kind: "name", value: pdfValue{kind: "name"}})
			i = j
		case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
			j := i
			for j < len(data) && (data[j] == '-' || data[j] == '+' || data[j] == '.' || (data[j] >= '0' && data[j] <= '9') || data[j] == 'e' || data[j] == 'E') {
				j++
			}
			if number, err := strconv.ParseFloat(string(data[i:j]), 64); err == nil {
				tokens = append(tokens, contentToken{kind: "num", value: pdfValue{kind: "num", number: number}})
			}
			i = j
		default:
			j := i
			for j < len(data) && !isContentDelim(data[j]) {
				j++
			}
			word := string(data[i:j])
			i = j
			if word == "" {
				continue
			}
			if isShowOperator(word) {
				tokens = append(tokens, contentToken{kind: "op", text: word})
			} else if isTrackedOperator(word) {
				tokens = append(tokens, contentToken{kind: "op", text: word})
			}
		}
	}
	return tokens
}

func isContentDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\f', 0, '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isShowOperator(word string) bool {
	switch word {
	case "Tj", "TJ", "'", "\"":
		return true
	}
	return false
}

func isTrackedOperator(word string) bool {
	switch word {
	case "Tf", "Tm", "Td", "TD", "TL", "T*", "BT", "ET":
		return true
	}
	return false
}

func scanLiteral(data []byte, start int) ([]byte, int) {
	var out []byte
	i := start + 1
	depth := 1
	for i < len(data) && depth > 0 {
		c := data[i]
		switch {
		case c == '\\' && i+1 < len(data):
			i++
			e := data[i]
			switch {
			case e == 'n':
				out = append(out, '\n')
			case e == 'r':
				out = append(out, '\r')
			case e == 't':
				out = append(out, '\t')
			case e == 'b':
				out = append(out, '\b')
			case e == 'f':
				out = append(out, '\f')
			case e == '(' || e == ')' || e == '\\':
				out = append(out, e)
			case e >= '0' && e <= '7':
				j := i
				for j < len(data) && j < i+3 && data[j] >= '0' && data[j] <= '7' {
					j++
				}
				var value int
				fmt.Sscanf(string(data[i:j]), "%o", &value)
				out = append(out, byte(value))
				i = j - 1
			case e == '\r' || e == '\n':
				// Line continuation contributes nothing.
			default:
				out = append(out, e)
			}
		case c == '(':
			depth++
			out = append(out, c)
		case c == ')':
			depth--
			if depth > 0 {
				out = append(out, c)
			}
		default:
			out = append(out, c)
		}
		i++
	}
	return out, i
}

func scanHex(data []byte, start int) ([]byte, int) {
	var digits []byte
	i := start + 1
	for i < len(data) && data[i] != '>' {
		c := data[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			digits = append(digits, c)
		}
		i++
	}
	if i < len(data) {
		i++
	}
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	out := make([]byte, 0, len(digits)/2)
	for j := 0; j < len(digits); j += 2 {
		hi := hexVal(digits[j])
		lo := hexVal(digits[j+1])
		out = append(out, hi<<4|lo)
	}
	return out, i
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func scanArray(data []byte, start int) (pdfValue, int) {
	var items []pdfValue
	i := start + 1
	for i < len(data) {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0:
			i++
		case c == ']':
			return pdfValue{kind: "array", array: items}, i + 1
		case c == '(':
			raw, next := scanLiteral(data, i)
			items = append(items, pdfValue{kind: "str", bytes: raw})
			i = next
		case c == '<':
			raw, next := scanHex(data, i)
			items = append(items, pdfValue{kind: "str", bytes: raw})
			i = next
		case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
			j := i
			for j < len(data) && (data[j] == '-' || data[j] == '+' || data[j] == '.' || (data[j] >= '0' && data[j] <= '9')) {
				j++
			}
			if number, err := strconv.ParseFloat(string(data[i:j]), 64); err == nil {
				items = append(items, pdfValue{kind: "num", number: number})
			}
			i = j
		default:
			i++
		}
	}
	return pdfValue{kind: "array", array: items}, i
}

func fragmentWidth(text string, size float64) float64 {
	return float64(len([]rune(text))) * size * pdfCharWidthFactor
}

// --- Layout recovery (mirrors the Python geometry heuristics) ---

func groupPDFLines(fragments []Fragment) [][]Fragment {
	ordered := append([]Fragment(nil), fragments...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Y != ordered[j].Y {
			return ordered[i].Y > ordered[j].Y
		}
		return ordered[i].X < ordered[j].X
	})
	var lines [][]Fragment
	for _, fragment := range ordered {
		if len(lines) > 0 && abs(lines[len(lines)-1][0].Y-fragment.Y) <= pdfLineTolerancePoints {
			lines[len(lines)-1] = append(lines[len(lines)-1], fragment)
		} else {
			lines = append(lines, []Fragment{fragment})
		}
	}
	for _, line := range lines {
		sort.Slice(line, func(i, j int) bool { return line[i].X < line[j].X })
	}
	return lines
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func columnBoundaries(fragments []Fragment) []float64 {
	if len(fragments) < 4 {
		return nil
	}
	ordered := append([]Fragment(nil), fragments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].X < ordered[j].X })
	clusters := [][]Fragment{{ordered[0]}}
	for _, fragment := range ordered[1:] {
		last := clusters[len(clusters)-1]
		if fragment.X-last[len(last)-1].X > pdfClusterGapPoints {
			clusters = append(clusters, []Fragment{fragment})
		} else {
			clusters[len(clusters)-1] = append(last, fragment)
		}
	}
	if len(clusters) < 2 {
		return nil
	}
	for _, cluster := range clusters {
		if len(cluster) < 2 {
			return nil
		}
	}
	var boundaries []float64
	for i := 0; i+1 < len(clusters); i++ {
		lastX := clusters[i][len(clusters[i])-1].X
		firstX := clusters[i+1][0].X
		boundaries = append(boundaries, (lastX+firstX)/2)
	}
	return boundaries
}

type columnRun struct {
	column int
	line   []Fragment
}

func orderedPDFLines(lines [][]Fragment) []columnRun {
	var fragments []Fragment
	for _, line := range lines {
		fragments = append(fragments, line...)
	}
	boundaries := columnBoundaries(fragments)
	byY := append([][]Fragment(nil), lines...)
	sort.Slice(byY, func(i, j int) bool { return byY[i][0].Y > byY[j][0].Y })
	if len(boundaries) != 1 {
		ordered := make([]columnRun, 0, len(byY))
		for _, line := range byY {
			ordered = append(ordered, columnRun{column: 0, line: line})
		}
		return ordered
	}
	boundary := boundaries[0]
	columnOf := func(x float64) int {
		if x > boundary {
			return 1
		}
		return 0
	}
	var ordered []columnRun
	for _, line := range lines {
		run := []Fragment{line[0]}
		for _, fragment := range line[1:] {
			if columnOf(fragment.X) != columnOf(run[0].X) {
				ordered = append(ordered, columnRun{column: columnOf(run[0].X), line: run})
				run = []Fragment{fragment}
			} else {
				run = append(run, fragment)
			}
		}
		ordered = append(ordered, columnRun{column: columnOf(run[0].X), line: run})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].column != ordered[j].column {
			return ordered[i].column < ordered[j].column
		}
		return ordered[i].line[0].Y > ordered[j].line[0].Y
	})
	return ordered
}

func pdfPageBlocks(fragments []Fragment, fallbackText string, pageNumber, previousBlockCount int) []Block {
	if len(fragments) == 0 {
		return []Block{EvidenceBlock(pageNumber, previousBlockCount+1, fallbackText, "pdf_text", nil)}
	}
	lines := groupPDFLines(fragments)
	ordered := orderedPDFLines(lines)
	var sizes []float64
	for _, line := range lines {
		for _, fragment := range line {
			sizes = append(sizes, fragment.Size)
		}
	}
	mergeGap := -1.0
	if len(sizes) > 0 {
		mergeGap = medianFloat(sizes) * pdfBlockMergeFactor
	}
	var blocks []Block
	var lineTexts []string
	var bbox []float64
	previousColumn := -1
	previousY := 0.0
	hasPrevious := false

	flush := func() {
		text := strings.TrimSpace(strings.Join(lineTexts, "\n"))
		if text != "" {
			blocks = append(blocks, EvidenceBlock(pageNumber, previousBlockCount+len(blocks)+1, text, "pdf_text", bbox))
		}
		lineTexts = nil
		bbox = nil
	}

	for _, run := range ordered {
		startedNew := hasPrevious && (run.column != previousColumn ||
			(mergeGap >= 0 && previousY-run.line[0].Y > mergeGap))
		if startedNew {
			flush()
		}
		var parts []string
		for _, fragment := range run.line {
			parts = append(parts, fragment.Text)
		}
		lineTexts = append(lineTexts, strings.Join(parts, " "))
		segment := [4]float64{
			minX(run.line), minY(run.line) - run.line[0].Size,
			maxX(run.line), maxY(run.line) + run.line[0].Size,
		}
		if bbox == nil {
			bbox = segment[:]
		} else {
			bbox = []float64{
				math_min(bbox[0], segment[0]), math_min(bbox[1], segment[1]),
				math_max(bbox[2], segment[2]), math_max(bbox[3], segment[3]),
			}
		}
		previousColumn = run.column
		previousY = run.line[0].Y
		hasPrevious = true
	}
	flush()
	return blocks
}

func medianFloat(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}

func minX(line []Fragment) float64 {
	best := line[0].X
	for _, fragment := range line[1:] {
		if fragment.X < best {
			best = fragment.X
		}
	}
	return best
}

func minY(line []Fragment) float64 {
	best := line[0].Y
	for _, fragment := range line[1:] {
		if fragment.Y < best {
			best = fragment.Y
		}
	}
	return best
}

func maxX(line []Fragment) float64 {
	best := line[0].X + fragmentWidth(line[0].Text, line[0].Size)
	for _, fragment := range line[1:] {
		if candidate := fragment.X + fragmentWidth(fragment.Text, fragment.Size); candidate > best {
			best = candidate
		}
	}
	return best
}

func maxY(line []Fragment) float64 {
	best := line[0].Y
	for _, fragment := range line[1:] {
		if fragment.Y > best {
			best = fragment.Y
		}
	}
	return best
}

func math_min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func math_max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
