// Package skills holds the concrete AI skills run by package harness. Each
// skill's prompt template is in package prompts.
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// RecipeText is what normalize_recipe reads: the parts of a recipe page's
// schema.org Recipe (see recipes.Extract) that it normalizes.
type RecipeText struct {
	Title string
	// Ingredients are the raw ingredient lines, in order. There is at
	// least one and none is blank.
	Ingredients []string
	// Steps are the instructions, in order; there may be none.
	Steps []string
	// PrepMinutes, CookMinutes and TotalMinutes are the JSON-LD times,
	// nil when the page gives none.
	PrepMinutes  *int
	CookMinutes  *int
	TotalMinutes *int
}

// Ingredient is one normalized ingredient line. Qty, Unit and Note are
// empty when the line gives none.
type Ingredient struct {
	Name string `json:"name"`
	Qty  string `json:"qty"`
	Unit string `json:"unit"`
	Note string `json:"note"`
}

// NormalizedRecipe is normalize_recipe's output.
type NormalizedRecipe struct {
	// Ingredients holds one ingredient per input line, in the same order.
	Ingredients []Ingredient `json:"ingredients"`
	// Method is one of stovetop, oven, crockpot, grill and other.
	Method string `json:"method"`
	// Difficulty is easy, medium or hard. The model may leave it empty;
	// NormalizeRecipe always returns one.
	Difficulty string `json:"difficulty"`
	// MinutesTotal is prep to table in minutes, nil when unknown.
	MinutesTotal *int `json:"minutes_total"`
}

// NormalizeRecipe runs normalize_recipe v1 on in and returns its output.
// The run is at temperature 0 whatever cfg.Settings.Temperature says: the
// same page must normalize the same way every time. scope names the recipe,
// e.g. its source URL (see harness.Run).
//
// Values the code can derive win over the model's: a positive JSON-LD
// TotalMinutes is minutes_total; a title or step mentioning a slow cooker
// or crock pot makes the method crockpot; and a difficulty the model left
// out is derived from the step count and minutes.
func NormalizeRecipe(ctx context.Context, cfg harness.Config, scope string, in RecipeText) (NormalizedRecipe, error) {
	cfg.Settings.Temperature = 0
	return harness.Run(ctx, cfg, normalizeRecipe{in: in}, scope, in)
}

// normalizeRecipe is the skill. It holds its input because validation and
// post-processing compare the output with it.
type normalizeRecipe struct {
	in RecipeText
}

var _ harness.Skill[RecipeText, NormalizedRecipe] = normalizeRecipe{}

func (normalizeRecipe) Name() string { return "normalize_recipe" }

func (normalizeRecipe) Version() int { return 1 }

func (normalizeRecipe) Timeout() time.Duration { return harness.DefaultTimeout }

// recipePrompt is the template data of normalize_recipe.v1.tmpl. Lists are
// numbered and every line is on one line.
type recipePrompt struct {
	Title       string
	Times       string
	Ingredients []string
	Steps       []string
}

func (normalizeRecipe) PromptInput(in RecipeText) (harness.PromptInput, error) {
	title := oneLine(in.Title)
	if title == "" {
		return harness.PromptInput{}, errors.New("recipe has no title")
	}
	if len(in.Ingredients) == 0 {
		return harness.PromptInput{}, errors.New("recipe has no ingredient lines")
	}
	p := recipePrompt{Title: title}
	for i, line := range in.Ingredients {
		line = oneLine(line)
		if line == "" {
			return harness.PromptInput{}, fmt.Errorf("ingredient line %d is blank", i+1)
		}
		p.Ingredients = append(p.Ingredients, fmt.Sprintf("%d. %s", i+1, line))
	}
	for i, step := range in.Steps {
		step = oneLine(step)
		if step == "" {
			return harness.PromptInput{}, fmt.Errorf("step %d is blank", i+1)
		}
		p.Steps = append(p.Steps, fmt.Sprintf("%d. %s", i+1, step))
	}
	var times []string
	for _, t := range []struct {
		label   string
		minutes *int
	}{{"prep", in.PrepMinutes}, {"cook", in.CookMinutes}, {"total", in.TotalMinutes}} {
		if t.minutes != nil {
			times = append(times, fmt.Sprintf("%s %d min", t.label, *t.minutes))
		}
	}
	p.Times = strings.Join(times, ", ")
	return harness.PromptInput{Data: p}, nil
}

// oneLine collapses every run of white space in s, line breaks included,
// to one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (normalizeRecipe) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "normalize_recipe", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"ingredients": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"qty": {"type": "string"},
						"unit": {"type": "string"},
						"note": {"type": "string"}
					},
					"required": ["name", "qty", "unit", "note"],
					"additionalProperties": false
				}
			},
			"method": {"type": "string", "enum": ["stovetop", "oven", "crockpot", "grill", "other"]},
			"difficulty": {"type": ["string", "null"], "enum": ["easy", "medium", "hard", null]},
			"minutes_total": {"type": ["integer", "null"]}
		},
		"required": ["ingredients", "method", "difficulty", "minutes_total"],
		"additionalProperties": false
	}`)}
}

var (
	methods      = []string{"stovetop", "oven", "crockpot", "grill", "other"}
	difficulties = []string{"easy", "medium", "hard"}
)

func (s normalizeRecipe) Validate(out NormalizedRecipe) []string {
	var problems []string
	lines := s.in.Ingredients
	if len(out.Ingredients) != len(lines) {
		problems = append(problems, fmt.Sprintf("ingredients has %d entries but there are %d ingredient lines; give exactly one ingredient per line, in order",
			len(out.Ingredients), len(lines)))
	}
	for i, ing := range out.Ingredients {
		if strings.TrimSpace(ing.Name) == "" {
			problems = append(problems, fmt.Sprintf("ingredient %d has an empty name; name the ingredient", i+1))
		}
	}
	if len(out.Ingredients) == len(lines) {
		problems = append(problems, orderProblems(out.Ingredients, lines)...)
	}
	if !slices.Contains(methods, out.Method) {
		problems = append(problems, fmt.Sprintf("method %q is not one of %s", out.Method, strings.Join(methods, ", ")))
	}
	if out.Difficulty != "" && !slices.Contains(difficulties, out.Difficulty) {
		problems = append(problems, fmt.Sprintf("difficulty %q is not one of %s, or null", out.Difficulty, strings.Join(difficulties, ", ")))
	}
	if out.MinutesTotal != nil && *out.MinutesTotal <= 0 && jsonLDMinutes(s.in) == nil {
		problems = append(problems, fmt.Sprintf("minutes_total %d is not positive; give the minutes or null", *out.MinutesTotal))
	}
	return problems
}

// orderProblems finds ingredients out of input order: an ingredient whose
// name shares no word with its own line but does with another line.
// Ingredients that match no line at all, e.g. a renamed one, are accepted.
func orderProblems(ings []Ingredient, lines []string) []string {
	var problems []string
	for i, ing := range ings {
		name := words(ing.Name)
		if len(name) == 0 || sharesWord(name, words(lines[i])) {
			continue
		}
		for j, line := range lines {
			if j != i && sharesWord(name, words(line)) {
				problems = append(problems, fmt.Sprintf("ingredient %d %q matches line %d %q, not line %d %q; keep the ingredients in line order",
					i+1, ing.Name, j+1, line, i+1, lines[i]))
				break
			}
		}
	}
	return problems
}

// words returns the lower-case words of s of three or more letters.
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len([]rune(w)) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// sharesWord reports whether a word of a and a word of b are the same up
// to a suffix, as "tomato" and "tomatoes" are.
func sharesWord(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.HasPrefix(x, y) || strings.HasPrefix(y, x) {
				return true
			}
		}
	}
	return false
}

// slowCooker matches a mention of a slow cooker or crock pot.
var slowCooker = regexp.MustCompile(`(?i)\b(slow[\s-]?cooker|crock[\s-]?pot)`)

func (s normalizeRecipe) PostProcess(out NormalizedRecipe) (NormalizedRecipe, error) {
	ings := make([]Ingredient, len(out.Ingredients))
	for i, ing := range out.Ingredients {
		ings[i] = Ingredient{
			Name: oneLine(ing.Name),
			Qty:  oneLine(ing.Qty),
			Unit: oneLine(ing.Unit),
			Note: oneLine(ing.Note),
		}
	}
	out.Ingredients = ings
	if m := jsonLDMinutes(s.in); m != nil {
		out.MinutesTotal = m
	}
	if mentionsSlowCooker(s.in) {
		out.Method = "crockpot"
	}
	if out.Difficulty == "" {
		minutes := 0
		if out.MinutesTotal != nil {
			minutes = *out.MinutesTotal
		}
		out.Difficulty = deriveDifficulty(len(s.in.Steps), minutes, out.Method == "crockpot")
	}
	return out, nil
}

// jsonLDMinutes returns a copy of the JSON-LD total time, or nil when the
// page gives no positive one.
func jsonLDMinutes(in RecipeText) *int {
	if in.TotalMinutes == nil || *in.TotalMinutes <= 0 {
		return nil
	}
	m := *in.TotalMinutes
	return &m
}

func mentionsSlowCooker(in RecipeText) bool {
	if slowCooker.MatchString(in.Title) {
		return true
	}
	for _, step := range in.Steps {
		if slowCooker.MatchString(step) {
			return true
		}
	}
	return false
}

// deriveDifficulty grades a recipe by its step count and minutes, 0 when
// unknown. A crockpot recipe's minutes are mostly unattended, so they do
// not count.
func deriveDifficulty(steps, minutes int, crockpot bool) string {
	if crockpot {
		minutes = 0
	}
	switch {
	case steps > 10 || minutes > 90:
		return "hard"
	case steps <= 5 && minutes <= 45:
		return "easy"
	default:
		return "medium"
	}
}
