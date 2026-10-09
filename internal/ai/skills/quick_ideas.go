package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// Kinds of Quick dinner quick_ideas suggests: the two tabs of the Quick
// dinners screen.
const (
	// QuickCrockpot is a dinner cooked in a crock pot (slow cooker).
	QuickCrockpot = "crockpot"
	// QuickUnder30 is a dinner 30 minutes or less from prep to table.
	QuickUnder30 = "under30"
)

// QuickKinds are every kind of Quick dinner, in the screen's tab order.
var QuickKinds = []string{QuickCrockpot, QuickUnder30}

const (
	// QuickIdeasRequested is how many ideas quick_ideas asks the model
	// for: more than a page holds, since validation, the exclude list,
	// grounding and the kind's check drop some.
	QuickIdeasRequested = 12
	// QuickIdeasKept is the most ideas one page of Quick dinners shows.
	QuickIdeasKept = 6
	// MaxExclude is the most names already shown a request may exclude.
	MaxExclude = 60
	// ExcludeLimit is the most characters one excluded name may have.
	ExcludeLimit = 120
)

// QuickIdeasRequest is what quick_ideas reads.
type QuickIdeasRequest struct {
	// Context is the household context (see harness.BuildContext): the
	// ideas are picked from the household's tastes.
	Context harness.HouseholdContext
	// Kind is QuickCrockpot or QuickUnder30. It is a hint to the model:
	// the caller enforces it after grounding, from the grounded recipe.
	Kind string
	// Exclude are the names already shown to the household, as
	// CleanExclude returns them. No idea repeating one is returned.
	Exclude []string
}

// QuickIdea is one Quick dinner idea. Method, EstMinutes and Difficulty
// are the model's estimates until the idea is grounded in a recipe.
type QuickIdea struct {
	Name       string `json:"name"`
	EstMinutes int    `json:"est_minutes"`
	// Method is one of stovetop, oven, crockpot, grill and other.
	Method string `json:"method"`
	// Difficulty is easy, medium or hard.
	Difficulty string `json:"difficulty"`
	// SearchQuery is a web search query that finds a recipe for the dinner.
	SearchQuery string `json:"search_query"`
}

// QuickIdeaList is quick_ideas' output.
type QuickIdeaList struct {
	// Ideas are the ideas that passed every rule, in the model's order.
	Ideas []QuickIdea `json:"ideas"`
	// Dropped are the ideas the model gave that broke a rule, in the
	// model's order. It is never part of the model's reply.
	Dropped []DroppedIdea `json:"-"`
}

// QuickIdeas runs quick_ideas v1 on in and returns ideas that repeat no
// excluded name nor each other (harness.Similar, on the normalized
// names); the rest are in Dropped. Whether an idea is of in.Kind is not
// decided here: the model's estimates never decide it, the grounded
// recipe does. scope names the request (see harness.Run; QuickIdeasScope).
// A kind CheckQuickKind refuses, or an exclude list CleanExclude refuses,
// fails before any LLM call.
func QuickIdeas(ctx context.Context, cfg harness.Config, scope string, in QuickIdeasRequest) (QuickIdeaList, error) {
	if err := CheckQuickKind(in.Kind); err != nil {
		return QuickIdeaList{}, fmt.Errorf("skills: quick_ideas: %w", err)
	}
	exclude, err := CleanExclude(in.Exclude)
	if err != nil {
		return QuickIdeaList{}, fmt.Errorf("skills: quick_ideas: %w", err)
	}
	in.Exclude = exclude
	return harness.Run(ctx, cfg, quickIdeas{in: in}, scope, in)
}

// QuickIdeasScope is the harness scope of a request: its kind and its
// normalized excluded names, in order, so two requests share a run only
// when they ask for the same page.
func QuickIdeasScope(in QuickIdeasRequest) string {
	parts := make([]string, len(in.Exclude))
	for i, name := range in.Exclude {
		parts[i] = harness.Normalize(name)
	}
	return "kind=" + in.Kind + ";" + strings.Join(parts, "|")
}

// CheckQuickKind returns why kind is not one of QuickKinds, or nil. There
// is no default kind.
func CheckQuickKind(kind string) error {
	if !slices.Contains(QuickKinds, kind) {
		return fmt.Errorf("kind %q is not one of %s", kind, strings.Join(QuickKinds, ", "))
	}
	return nil
}

// CleanExclude returns the excluded names with each on one line, never
// nil, or why they cannot be used: there are more than MaxExclude, or a
// name is blank, longer than ExcludeLimit characters, has nothing left
// once normalized (harness.Normalize), or normalizes the same as an
// earlier one. An empty list, the first page's, is valid.
func CleanExclude(list []string) ([]string, error) {
	if len(list) > MaxExclude {
		return nil, fmt.Errorf("%d names excluded; exclude at most %d", len(list), MaxExclude)
	}
	out := make([]string, len(list))
	seen := map[string]string{}
	for i, name := range list {
		name = oneLine(name)
		switch {
		case name == "":
			return nil, fmt.Errorf("excluded name %d is blank", i+1)
		case utf8.RuneCountInString(name) > ExcludeLimit:
			return nil, fmt.Errorf("excluded name %d is over %d characters", i+1, ExcludeLimit)
		}
		n := harness.Normalize(name)
		if n == "" {
			return nil, fmt.Errorf("excluded name %d, %q, names no dinner", i+1, name)
		}
		if first, ok := seen[n]; ok {
			return nil, fmt.Errorf("excluded name %d, %q, repeats %q", i+1, name, first)
		}
		seen[n] = name
		out[i] = name
	}
	return out, nil
}

// quickIdeas is the skill. It holds its input because post-processing
// compares the output with it.
type quickIdeas struct {
	in QuickIdeasRequest
}

var _ harness.Skill[QuickIdeasRequest, QuickIdeaList] = quickIdeas{}

func (quickIdeas) Name() string { return "quick_ideas" }

func (quickIdeas) Version() int { return 1 }

func (quickIdeas) Timeout() time.Duration { return harness.DefaultTimeout }

// quickIdeasPrompt is the template data of quick_ideas.v1.tmpl.
type quickIdeasPrompt struct {
	// Count is how many ideas to ask for.
	Count int
	// Crockpot is set for QuickCrockpot, unset for QuickUnder30.
	Crockpot bool
}

// PromptInput puts the household context, then the names already shown,
// in the household context block.
func (quickIdeas) PromptInput(in QuickIdeasRequest) (harness.PromptInput, error) {
	if err := CheckQuickKind(in.Kind); err != nil {
		return harness.PromptInput{}, err
	}
	sections := append(in.Context.Sections(), harness.ContextSection{Title: "Already shown", Lines: in.Exclude})
	return harness.PromptInput{
		Data:    quickIdeasPrompt{Count: QuickIdeasRequested, Crockpot: in.Kind == QuickCrockpot},
		Context: sections,
	}, nil
}

func (quickIdeas) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "quick_ideas", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"ideas": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"est_minutes": {"type": "integer"},
						"method": {"type": "string", "enum": ["stovetop", "oven", "crockpot", "grill", "other"]},
						"difficulty": {"type": "string", "enum": ["easy", "medium", "hard"]},
						"search_query": {"type": "string"}
					},
					"required": ["name", "est_minutes", "method", "difficulty", "search_query"],
					"additionalProperties": false
				}
			}
		},
		"required": ["ideas"],
		"additionalProperties": false
	}`)}
}

// UnmarshalJSON decodes an idea, requiring each of its fields as the
// schema does: encoding/json would otherwise read a missing or null one as
// empty.
func (i *QuickIdea) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "name", "est_minutes", "method", "difficulty", "search_query"); err != nil {
		return fmt.Errorf("idea %s", err)
	}
	type plain QuickIdea
	return json.Unmarshal(data, (*plain)(i))
}

// UnmarshalJSON decodes the output, requiring its one field as the schema
// does.
func (out *QuickIdeaList) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "ideas"); err != nil {
		return fmt.Errorf("output %s", err)
	}
	type plain QuickIdeaList
	return json.Unmarshal(data, (*plain)(out))
}

// Validate finds what makes the reply unusable as a whole: no ideas, too
// many, or one that is malformed. A well-formed idea that breaks a rule is
// dropped by PostProcess instead.
func (quickIdeas) Validate(out QuickIdeaList) []string {
	var problems []string
	switch n := len(out.Ideas); {
	case n == 0 && len(out.Dropped) == 0:
		problems = append(problems, fmt.Sprintf("ideas is empty; give %d ideas", QuickIdeasRequested))
	case n > QuickIdeasRequested:
		problems = append(problems, fmt.Sprintf("ideas has %d entries; give at most %d ideas", n, QuickIdeasRequested))
	}
	for i, idea := range out.Ideas {
		for _, f := range []struct{ field, value string }{{"name", idea.Name}, {"search_query", idea.SearchQuery}} {
			if strings.TrimSpace(f.value) == "" {
				problems = append(problems, fmt.Sprintf("idea %d has an empty %s; give one", i+1, f.field))
			}
		}
		if strings.TrimSpace(idea.Name) != "" && harness.Normalize(idea.Name) == "" {
			problems = append(problems, fmt.Sprintf("idea %d name %q has only filler words; name the dish", i+1, idea.Name))
		}
		if !slices.Contains(methods, idea.Method) {
			problems = append(problems, fmt.Sprintf("idea %d method %q is not one of %s", i+1, idea.Method, strings.Join(methods, ", ")))
		}
		if idea.EstMinutes <= 0 {
			problems = append(problems, fmt.Sprintf("idea %d est_minutes %d is not positive; give the minutes from prep to table", i+1, idea.EstMinutes))
		}
		if !slices.Contains(difficulties, idea.Difficulty) {
			problems = append(problems, fmt.Sprintf("idea %d difficulty %q is not one of %s", i+1, idea.Difficulty, strings.Join(difficulties, ", ")))
		}
	}
	return problems
}

// PostProcess puts every idea's text on one line and moves each that
// repeats an excluded name or an earlier idea (harness.Similar, which
// compares normalized names) to Dropped. The model's method and minutes
// are kept as estimates and never decide the kind.
func (s quickIdeas) PostProcess(out QuickIdeaList) (QuickIdeaList, error) {
	result := QuickIdeaList{Ideas: []QuickIdea{}}
	for _, idea := range out.Ideas {
		idea = QuickIdea{
			Name:        oneLine(idea.Name),
			EstMinutes:  idea.EstMinutes,
			Method:      idea.Method,
			Difficulty:  idea.Difficulty,
			SearchQuery: oneLine(idea.SearchQuery),
		}
		if why := s.drop(idea, result.Ideas); why != "" {
			result.Dropped = append(result.Dropped, DroppedIdea{Name: idea.Name, Why: why})
			continue
		}
		result.Ideas = append(result.Ideas, idea)
	}
	return result, nil
}

// drop returns why idea must be dropped, or "" when it may be kept.
func (s quickIdeas) drop(idea QuickIdea, kept []QuickIdea) string {
	for _, name := range s.in.Exclude {
		if harness.Similar(idea.Name, name) {
			return fmt.Sprintf("repeats %q, already shown", name)
		}
	}
	for _, k := range kept {
		if harness.Similar(idea.Name, k.Name) {
			return fmt.Sprintf("repeats the idea %q", k.Name)
		}
	}
	return ""
}
