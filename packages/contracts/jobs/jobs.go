// Package jobs defines the durable work contracts shared by queue producers and workers.
package jobs

import (
	"fmt"
	"sort"
	"strings"
)

type Type string

const (
	ResumeParse                 Type = "resume_parse"
	ResumeEnrich                Type = "resume_enrich"
	JobDescriptionParse         Type = "job_description_parse"
	JobDescriptionEnrich        Type = "job_description_enrich"
	IndependentEvaluationParse  Type = "independent_evaluation_parse"
	IndependentEvaluationEnrich Type = "independent_evaluation_enrich"
	EvaluationProcess           Type = "evaluation_process"
)

type Role string

const (
	ParserRole    Role = "parser"
	InferenceRole Role = "inference"
)

var roleTypes = map[Role][]Type{
	ParserRole: {
		ResumeParse,
		JobDescriptionParse,
		IndependentEvaluationParse,
	},
	InferenceRole: {
		ResumeEnrich,
		JobDescriptionEnrich,
		IndependentEvaluationEnrich,
		EvaluationProcess,
	},
}

func ParseRole(value string) (Role, error) {
	role := Role(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := roleTypes[role]; !ok {
		return "", fmt.Errorf("WORKER_ROLE must be parser or inference")
	}
	return role, nil
}

func TypesFor(role Role) []string {
	types := roleTypes[role]
	out := make([]string, 0, len(types))
	for _, jobType := range types {
		out = append(out, string(jobType))
	}
	sort.Strings(out)
	return out
}

func OwnedBy(role Role, jobType Type) bool {
	for _, candidate := range roleTypes[role] {
		if candidate == jobType {
			return true
		}
	}
	return false
}
