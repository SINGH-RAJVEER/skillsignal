package documents

import "testing"

func TestJobDescriptionFormatsDoNotExpandResumeFormats(t *testing.T) {
	for _, filename := range []string{"job.md", "job.markdown", "job.html", "job.htm"} {
		t.Run(filename, func(t *testing.T) {
			accepted, err := ValidateJobDescription([]byte("Required: Go experience"), "application/octet-stream", filename)
			if err != nil || accepted.Extension == "" {
				t.Fatalf("job description rejected: %+v, %v", accepted, err)
			}
			if _, err := ValidateDocument([]byte("Resume"), accepted.MediaType, filename); err == nil {
				t.Fatal("job description format accepted as resume")
			}
		})
	}
}

func TestJobDescriptionRejectsDisguisedBinary(t *testing.T) {
	for _, content := range [][]byte{nil, {0xff}, {'a', 0, 'b'}} {
		if _, err := ValidateJobDescription(content, "text/markdown", "job.md"); err == nil {
			t.Fatal("non-text job description accepted")
		}
	}
	if _, err := ValidateJobDescription([]byte("description"), "application/pdf", "job.html"); err == nil {
		t.Fatal("mismatched media type accepted")
	}
}
