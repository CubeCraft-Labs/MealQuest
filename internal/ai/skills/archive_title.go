package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// TitleLimit is the most characters a Supply run's title may have.
const TitleLimit = 48

// ArchiveTitleRequest is what archive_title reads.
type ArchiveTitleRequest struct {
	// Dinners are the names of the week's planned dinners in day order,
	// Monday first. It must hold at least one, none blank.
	Dinners []string
}

// ArchiveTitle is archive_title's output.
type ArchiveTitle struct {
	// Title is the Supply run's post title in the Supply archive: 1 to
	// TitleLimit characters on one line, with no emoji and no surrounding
	// quotation marks.
	Title string `json:"title"`
}

// TitleArchive runs archive_title v1 on in and returns the title of the
// week's Supply run, written from its dinners. A request with no dinners,
// or a blank one, fails before any LLM call. scope names the week and its
// dinners (see harness.Run).
func TitleArchive(ctx context.Context, cfg harness.Config, scope string, in ArchiveTitleRequest) (ArchiveTitle, error) {
	return harness.Run(ctx, cfg, archiveTitle{}, scope, in)
}

// archiveTitle is the skill.
type archiveTitle struct{}

var _ harness.Skill[ArchiveTitleRequest, ArchiveTitle] = archiveTitle{}

func (archiveTitle) Name() string { return "archive_title" }

func (archiveTitle) Version() int { return 1 }

func (archiveTitle) Timeout() time.Duration { return harness.DefaultTimeout }

// archiveTitlePrompt is the template data of archive_title.v1.tmpl.
type archiveTitlePrompt struct {
	// Dinners are the week's dinner names in day order, each on one line.
	Dinners []string
}

func (archiveTitle) PromptInput(in ArchiveTitleRequest) (harness.PromptInput, error) {
	if len(in.Dinners) == 0 {
		return harness.PromptInput{}, errors.New("the week has no dinners to title it from")
	}
	var p archiveTitlePrompt
	for i, name := range in.Dinners {
		name = oneLine(name)
		if name == "" {
			return harness.PromptInput{}, fmt.Errorf("dinner %d has a blank name", i+1)
		}
		p.Dinners = append(p.Dinners, name)
	}
	return harness.PromptInput{Data: p}, nil
}

func (archiveTitle) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "archive_title", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string"}
		},
		"required": ["title"],
		"additionalProperties": false
	}`)}
}

// UnmarshalJSON decodes the output, requiring its field as the schema
// does.
func (t *ArchiveTitle) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "title"); err != nil {
		return fmt.Errorf("output %s", err)
	}
	type plain ArchiveTitle
	return json.Unmarshal(data, (*plain)(t))
}

// quotes are the quotation marks a title may not start or end with.
const quotes = `"'` + "`" + "“”‘’„‚«»‹›「」『』"

// Validate finds every broken rule of the title as PostProcess will store
// it, on one line: empty, over TitleLimit characters, holding an emoji, or
// starting or ending with a quotation mark.
func (archiveTitle) Validate(out ArchiveTitle) []string {
	var problems []string
	title := oneLine(out.Title)
	switch n := utf8.RuneCountInString(title); {
	case n == 0:
		problems = append(problems, fmt.Sprintf("title is empty; give a title of 1 to %d characters", TitleLimit))
	case n > TitleLimit:
		problems = append(problems, fmt.Sprintf("title %q has %d characters; shorten it to at most %d", title, n, TitleLimit))
	}
	for _, r := range title {
		if isEmoji(r) {
			problems = append(problems, fmt.Sprintf("title %q has an emoji (%U); use words only", title, r))
			break
		}
	}
	if first, _ := utf8.DecodeRuneInString(title); title != "" && strings.ContainsRune(quotes, first) {
		problems = append(problems, fmt.Sprintf("title %q starts with a quotation mark; give the title without quotation marks around it", title))
	} else if last, _ := utf8.DecodeLastRuneInString(title); title != "" && strings.ContainsRune(quotes, last) {
		problems = append(problems, fmt.Sprintf("title %q ends with a quotation mark; give the title without quotation marks around it", title))
	}
	return problems
}

// isEmoji reports whether r is an emoji or a part of one: a pictograph,
// a symbol from the emoji blocks, a regional indicator, a keycap, a skin
// tone, an emoji variation selector or the zero-width joiner.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // tiles, cards, enclosed, pictographs, emoticons, transport, supplemental symbols
		return true
	case r >= 0x2600 && r <= 0x27BF: // miscellaneous symbols and dingbats
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // arrows and stars such as ⭐ and ⬛
		return true
	case r >= 0x2300 && r <= 0x23FF: // technical symbols such as ⌚ and ⏰
		return true
	case r >= 0xE0020 && r <= 0xE007F: // tag sequences of flags
		return true
	case r == 0x200D, r == 0x20E3, r == 0xFE0F: // joiner, keycap, emoji presentation
		return true
	}
	return false
}

// PostProcess puts the title on one line, collapsing its white space.
func (archiveTitle) PostProcess(out ArchiveTitle) (ArchiveTitle, error) {
	return ArchiveTitle{Title: oneLine(out.Title)}, nil
}
