// Package versions pins the artifact schema, prompt, parser, and scoring
// policy versions stamped on every stored evaluation artifact.
package versions

const (
	ResumeFactsSchemaVersion       = "3"
	JobRequirementsSchemaVersion   = "2"
	JobRequirementsPromptVersion   = "1"
	JobRequirementsCompilerVersion = "compiler-2"
	LocalParserVersion             = "local-2"
	ParserConfigurationVersion     = "resume-parser-2"
	ExtractionPromptVersion        = "3"
	RequirementAssessmentSchemaVer = "1"
	AssessmentPromptVersion        = "1"
	ScoringPolicyVersion           = "criterion-weighted-1"
)
