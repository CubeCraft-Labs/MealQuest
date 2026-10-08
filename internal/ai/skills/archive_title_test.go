package skills

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// titleWeek is a week of seven dinners, Monday first.
var titleWeek = ArchiveTitleRequest{Dinners: []string{
	"Beef Chili", "Pot Roast", "Chicken  Fajitas", "Spaghetti Bolognese", "Sheet Pan Salmon", "Pad Thai", "Fish Tacos",
}}

func titleReply(title string) llm.GenerateResult {
	return reply(fmt.Sprintf(`{"title":%q}`, title))
}

// title runs TitleArchive on in with f and checks f was called exactly
// calls times, each at the household temperature with the archive_title
// schema.
func title(t *testing.T, f *llm.Fake, in ArchiveTitleRequest, calls int) (ArchiveTitle, []llm.Request, error) {
	t.Helper()
	out, err := TitleArchive(context.Background(), testConfig(t, f), "2026-10-12", in)
	reqs := f.Requests()
	if len(reqs) != calls {
		t.Fatalf("Generate called %d times, want %d", len(reqs), calls)
	}
	if g, _, _ := f.Remaining(); g != 0 {
		t.Fatalf("%d scripted Generate results unused", g)
	}
	for i, req := range reqs {
		if req.Temperature == nil || *req.Temperature != 0.7 {
			t.Fatalf("request %d temperature = %v, want 0.7", i+1, req.Temperature)
		}
		if req.Schema == nil || req.Schema.Name != "archive_title" {
			t.Fatalf("request %d schema = %v, want archive_title", i+1, req.Schema)
		}
	}
	return out, reqs, err
}

func TestArchiveTitleValid(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(titleReply("  Slow Simmers\nand Taco Night "))
	out, reqs, err := title(t, &f, titleWeek, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Slow Simmers and Taco Night" {
		t.Fatalf("title = %q", out.Title)
	}
	system, user := reqs[0].System, reqs[0].Messages[0].Content
	golden(t, "archive_title_valid.system.golden", system)
	golden(t, "archive_title_valid.user.golden", user)
	want := "Title the week whose dinners are, in day order from Monday:\n" +
		"- Beef Chili\n- Pot Roast\n- Chicken Fajitas\n- Spaghetti Bolognese\n- Sheet Pan Salmon\n- Pad Thai\n- Fish Tacos"
	if user != want {
		t.Errorf("user message = %q, want %q", user, want)
	}
	if !strings.Contains(system, "## Output contract") || !strings.Contains(system, `"title"`) {
		t.Error("system prompt lacks the output contract")
	}
}

// titleRepaired checks a first reply is repaired once, quoting it and the
// problem wanted.
func titleRepaired(t *testing.T, first, want string) {
	t.Helper()
	var f llm.Fake
	f.QueueGenerate(titleReply(first), titleReply("Chili, Roast and Taco Night"))
	out, reqs, err := title(t, &f, titleWeek, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Chili, Roast and Taco Night" {
		t.Fatalf("title = %q", out.Title)
	}
	repair := reqs[1].Messages
	if len(repair) != 3 || repair[1].Content != fmt.Sprintf(`{"title":%q}`, first) {
		t.Fatalf("repair turn does not quote the first reply: %+v", repair)
	}
	if !strings.Contains(repair[2].Content, want) {
		t.Fatalf("repair message = %q, want it to contain %q", repair[2].Content, want)
	}
}

func TestArchiveTitleTooLongRepaired(t *testing.T) {
	long := "A Week of Slow Simmers, Sizzling Fajitas and Taco Night" // 55 characters
	titleRepaired(t, long, fmt.Sprintf("title %q has 55 characters; shorten it to at most 48", long))
}

func TestArchiveTitleEmojiRepaired(t *testing.T) {
	titleRepaired(t, "Taco Night 🌮", `title "Taco Night 🌮" has an emoji (U+1F32E); use words only`)
}

func TestArchiveTitleQuotedRepaired(t *testing.T) {
	titleRepaired(t, `"Taco Night"`, `title "\"Taco Night\"" starts with a quotation mark; give the title without quotation marks around it`)
}

func TestArchiveTitleEmojiTwiceFails(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(titleReply("Chili Week 🔥"), titleReply("Chili Week ❤️"))
	out, _, err := title(t, &f, titleWeek, 2)
	var bad *harness.BadOutputError
	if !errors.As(err, &bad) || !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want a BadOutputError", err)
	}
	if !slices.Equal(bad.Repair, []string{`title "Chili Week ❤️" has an emoji (U+2764); use words only`}) {
		t.Fatalf("repair problems = %q", bad.Repair)
	}
	if out.Title != "" {
		t.Fatalf("title = %q, want none", out.Title)
	}
}

func TestArchiveTitleRules(t *testing.T) {
	s := archiveTitle{}
	for _, tc := range []struct {
		title string
		want  []string
	}{
		{"Taco Night", nil},
		{strings.Repeat("a", TitleLimit), nil},
		{"Crème Brûlée & Co.", nil},
		{"Fajitas — Then Tacos", nil},
		{"Mom's Pot Roast Week", nil},
		{"  Spaced   out  ", nil},
		{"", []string{"title is empty; give a title of 1 to 48 characters"}},
		{" \n ", []string{"title is empty; give a title of 1 to 48 characters"}},
		{strings.Repeat("é", TitleLimit+1), []string{fmt.Sprintf("title %q has 49 characters; shorten it to at most 48", strings.Repeat("é", TitleLimit+1))}},
		{"Hot ☕ Week", []string{`title "Hot ☕ Week" has an emoji (U+2615); use words only`}},
		{"Star ⭐ Week", []string{`title "Star ⭐ Week" has an emoji (U+2B50); use words only`}},
		{"Go 🇮🇹", []string{`title "Go 🇮🇹" has an emoji (U+1F1EE); use words only`}},
		{"‘Taco Night’", []string{`title "‘Taco Night’" starts with a quotation mark; give the title without quotation marks around it`}},
		{"Taco Night”", []string{`title "Taco Night”" ends with a quotation mark; give the title without quotation marks around it`}},
		{"«Pasta» 🍝 " + strings.Repeat("x", 45), []string{
			fmt.Sprintf("title %q has 55 characters; shorten it to at most 48", "«Pasta» 🍝 "+strings.Repeat("x", 45)),
			fmt.Sprintf("title %q has an emoji (U+1F35D); use words only", "«Pasta» 🍝 "+strings.Repeat("x", 45)),
			fmt.Sprintf("title %q starts with a quotation mark; give the title without quotation marks around it", "«Pasta» 🍝 "+strings.Repeat("x", 45)),
		}},
	} {
		if got := s.Validate(ArchiveTitle{Title: tc.title}); !slices.Equal(got, tc.want) {
			t.Errorf("Validate(%q) =\n%q\nwant\n%q", tc.title, got, tc.want)
		}
	}
}

func TestArchiveTitleBadRequestFailsBeforeCall(t *testing.T) {
	for name, tc := range map[string]struct {
		in   ArchiveTitleRequest
		want string
	}{
		"no dinners":   {ArchiveTitleRequest{}, "the week has no dinners to title it from"},
		"blank dinner": {ArchiveTitleRequest{Dinners: []string{"Pad Thai", " \n"}}, "dinner 2 has a blank name"},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			_, _, err := title(t, &f, tc.in, 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
