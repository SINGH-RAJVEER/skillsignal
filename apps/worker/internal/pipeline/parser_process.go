package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"github.com/jackc/pgx/v5"
	parser "github.com/skillsignal/worker/internal/docparse"
)

// The child starts before worker configuration, without database or provider credentials.
func init() {
	if os.Getenv("SKILLSIGNAL_PARSE_CHILD") != "1" {
		return
	}
	var input struct {
		Content        []byte
		MediaType      string
		JobDescription bool
	}
	var result struct {
		Document parser.ParsedDocument
		Error    string
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(2)
	}
	var err error
	if input.JobDescription {
		result.Document, err = parser.ExtractJobDescription(input.Content, input.MediaType)
	} else {
		result.Document, err = parser.ExtractBlocks(input.Content, input.MediaType)
	}
	if err != nil {
		result.Error = "Document could not be parsed"
		if safe, ok := err.(*parser.ParseError); ok {
			result.Error = safe.Message
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func parseSubprocess(content []byte, mediaType string, timeout time.Duration, jobDescription bool) (parser.ParsedDocument, error) {
	executable, err := os.Executable()
	if err != nil {
		return parser.ParsedDocument{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	input, err := json.Marshal(struct {
		Content        []byte
		MediaType      string
		JobDescription bool
	}{content, mediaType, jobDescription})
	if err != nil {
		return parser.ParsedDocument{}, err
	}
	command := exec.CommandContext(ctx, executable)
	command.Env = []string{"SKILLSIGNAL_PARSE_CHILD=1", "GOMEMLIMIT=256MiB", "GOMAXPROCS=1"}
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if ctx.Err() != nil {
		return parser.ParsedDocument{}, &NonRetryableError{Message: "Document parsing exceeded the configured time limit"}
	}
	if err != nil {
		return parser.ParsedDocument{}, &NonRetryableError{Message: "Document parser failed"}
	}
	var result struct {
		Document parser.ParsedDocument
		Error    string
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return parser.ParsedDocument{}, err
	}
	if result.Error != "" {
		return parser.ParsedDocument{}, &NonRetryableError{Message: result.Error}
	}
	return result.Document, nil
}

func lockLease(ctx context.Context, tx pgx.Tx, job *ClaimedJob) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM processing_job WHERE id = $1 AND lease_token = $2 AND status = 'processing' AND lease_expires_at > now() FOR UPDATE`, job.ID, job.LeaseToken).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
