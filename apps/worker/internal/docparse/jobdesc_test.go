package parser

import (
	"strings"
	"testing"
)

func TestJobDescriptionFormats(t *testing.T) {
	for media, content := range map[string]string{
		"text/markdown": "## Requirements\n\n- Python experience is required to build software services for our engineering team.",
		"text/html":     "<html><head><title>Secret title</title><style>secret style</style></head><body><h2>Requirements</h2><p>Python <strong>experience</strong> is required to build software services for our engineering team.</p><script>secret script</script></body></html>",
	} {
		t.Run(media, func(t *testing.T) {
			doc, err := ExtractJobDescription([]byte(content), media)
			if err != nil {
				t.Fatal(err)
			}
			var texts []string
			for _, block := range doc.Blocks {
				texts = append(texts, block.Text)
			}
			text := strings.Join(texts, "\n")
			if doc.Quality.State != "ready" || !strings.Contains(text, "Requirements") || !strings.Contains(text, "Python") || strings.Contains(text, "secret") || strings.Contains(text, "<strong>") {
				t.Fatalf("unexpected document: %+v", doc)
			}
			if media == "text/html" && !strings.Contains(text, "Python experience is required") {
				t.Fatalf("inline markup broke sentence: %q", text)
			}
		})
	}
}
