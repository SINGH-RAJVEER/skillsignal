// Package vocab implements corpus-backed skill phrase matching.
//
// Phrases match longest-first over normalized tokens; absorber phrases
// (credential context such as degree names) are consumed without emitting.
package vocab

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed skills_corpus.json
var corpusJSON []byte

// TokenPattern keeps internal punctuation used by skill names: c++, c#,
// node.js, ci/cd, t-sql.
var TokenPattern = regexp.MustCompile(`[a-z0-9]+(?:[.#/-]+[a-z0-9]+|[+#]+)*`)

// MaxPhraseTokens bounds multi-word matching.
const MaxPhraseTokens = 6

// Vocabulary maps phrases to canonical skill names.
type Vocabulary struct {
	phraseToCanonical map[string]string
	categories        map[string]string
	absorbers         map[string]struct{}
	maxTokens         int
}

var (
	loadOnce sync.Once
	loaded   *Vocabulary
	loadErr  error
)

// Load parses the embedded corpus once.
func Load() (*Vocabulary, error) {
	loadOnce.Do(func() {
		var corpus struct {
			Skills []struct {
				Name     string  `json:"name"`
				Category *string `json:"category"`
			} `json:"skills"`
			Aliases   map[string]string `json:"aliases"`
			Absorbers []string          `json:"absorbers"`
		}
		if err := json.Unmarshal(corpusJSON, &corpus); err != nil {
			loadErr = err
			return
		}
		v := &Vocabulary{
			phraseToCanonical: make(map[string]string, len(corpus.Skills)+len(corpus.Aliases)),
			categories:        make(map[string]string, len(corpus.Skills)),
			absorbers:         make(map[string]struct{}, len(corpus.Absorbers)),
		}
		for _, skill := range corpus.Skills {
			key := strings.ToLower(skill.Name)
			if skill.Category != nil {
				v.categories[key] = *skill.Category
			}
			v.phraseToCanonical[key] = skill.Name
		}
		for alias, canonical := range corpus.Aliases {
			if _, ok := v.categories[strings.ToLower(canonical)]; !ok {
				loadErr = fmt.Errorf("alias %q targets unknown skill %q", alias, canonical)
				return
			}
			v.phraseToCanonical[strings.ToLower(alias)] = canonical
		}
		for _, absorber := range corpus.Absorbers {
			v.absorbers[strings.ToLower(absorber)] = struct{}{}
		}
		maxTokens := 0
		for phrase := range v.phraseToCanonical {
			if count := len(strings.Split(phrase, " ")); count > maxTokens {
				maxTokens = count
			}
		}
		if maxTokens > MaxPhraseTokens {
			maxTokens = MaxPhraseTokens
		}
		v.maxTokens = maxTokens
		loaded = v
	})
	return loaded, loadErr
}

// Mention matches skill phrases longest-first, returning canonical names
// mapped to the matched surface phrases.
func (v *Vocabulary) Mention(text string) map[string][]string {
	tokens := TokenPattern.FindAllString(strings.ToLower(text), -1)
	found := make(map[string][]string)
	index := 0
	for index < len(tokens) {
		matched := 0
		limit := v.maxTokens
		if remaining := len(tokens) - index; remaining < limit {
			limit = remaining
		}
		for count := limit; count > 0; count-- {
			phrase := strings.Join(tokens[index:index+count], " ")
			if canonical, ok := v.phraseToCanonical[phrase]; ok {
				spans := found[canonical]
				duplicate := false
				for _, span := range spans {
					if span == phrase {
						duplicate = true
						break
					}
				}
				if !duplicate {
					found[canonical] = append(spans, phrase)
				}
				matched = count
				break
			}
			if _, ok := v.absorbers[phrase]; ok {
				matched = count
				break
			}
		}
		if matched == 0 {
			matched = 1
		}
		index += matched
	}
	return found
}

// CategoryFor returns the taxonomy category of a canonical name.
func (v *Vocabulary) CategoryFor(canonical string) string {
	return v.categories[strings.ToLower(canonical)]
}

// CanonicalFor resolves a surface phrase to its canonical name.
func (v *Vocabulary) CanonicalFor(phrase string) (string, bool) {
	canonical, ok := v.phraseToCanonical[strings.ToLower(phrase)]
	return canonical, ok
}

// MentionedSkills returns canonical skill names in free text.
func MentionedSkills(text string) (map[string]struct{}, error) {
	v, err := Load()
	if err != nil {
		return nil, err
	}
	found := v.Mention(text)
	out := make(map[string]struct{}, len(found))
	for name := range found {
		out[name] = struct{}{}
	}
	return out, nil
}

// SortedNames returns canonical names in casefold order.
func SortedNames(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}
