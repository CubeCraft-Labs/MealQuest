package dinners_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
)

// TestCreateRegularNeverNamesFromLink: a Regular added with a recipe link
// is named only by the household, so no part of the link — host, path,
// query or fragment — is stored as its name or shown in a dinner or a
// duplicate error, even a token of plain letters in the path.
func TestCreateRegularNeverNamesFromLink(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	const link = "https://c.example/stew-key-abcdefghijklmno?sig=zyxwvut#lookup"
	// Each leak holds a character no ULID has (Crockford base32 has no I, L,
	// O, U, "-", "." or "/"), so a dinner's id never matches one.
	leaks := []string{link, "c.example", "stew-key", "abcdefghijklmno", "sig", "zyxwvut", "lookup"}
	check := func(what, text string) {
		t.Helper()
		lower := strings.ToLower(text)
		for _, leak := range leaks {
			if strings.Contains(lower, leak) {
				t.Errorf("%s contains %q: %s", what, leak, text)
			}
		}
	}

	_, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{RecipeURL: ptr(link)}, member)
	var verr *dinners.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("link without a name: err = %v, want a *ValidationError", err)
	}
	check("validation error", verr.Error())

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr(" Sunday stew "), RecipeURL: ptr(link)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Sunday stew" {
		t.Errorf("name = %q, want the household's %q", c.Name, "Sunday stew")
	}
	created, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	check("create response", string(created))
	row := e.dinner(t, c.ID)
	check("stored name", row.Name+" "+row.NormalizedName)
	if row.RecipeUrl.String != link {
		t.Errorf("stored recipe_url = %q, want %q kept for grounding", row.RecipeUrl.String, link)
	}

	list, err := e.svc.List(t.Context(), dinners.StatusRegular)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	check("list response", string(listed))

	// The same link under another name is still a duplicate, and its error
	// names the existing dinner by the household's name.
	_, err = e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Weeknight chili"), RecipeURL: ptr(link)}, member)
	var dup *dinners.DuplicateError
	if !errors.As(err, &dup) || dup.Existing.ID != c.ID {
		t.Fatalf("same link again: err = %v, want a duplicate of %s", err, c.ID)
	}
	check("duplicate error", dup.Error())
}
