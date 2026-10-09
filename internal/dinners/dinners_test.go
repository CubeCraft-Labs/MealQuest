package dinners_test

import (
	"database/sql"
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

// TestPageSiteNameWithholdsHosts: a stored site name that could be a host —
// the link's own, in any case and without "www.", an IP address or a dotted
// host name — is withheld; a name the page gave, dotless or with spaces, is
// kept.
func TestPageSiteNameWithholdsHosts(t *testing.T) {
	t.Parallel()
	const link = "https://www.TokenHost/recipes/stew"
	for _, tc := range []struct {
		stored string
		valid  bool
		want   *string
	}{
		{"", false, nil},
		{"tokenhost", true, nil},
		{"TOKENHOST", true, nil},
		{"private-token.example.com", true, nil},
		{"Food.com", true, nil},
		{"203.0.113.7", true, nil},
		{"2001:db8::1", true, nil},
		{"Delish", true, ptr("Delish")},
		{"Bon Appétit", true, ptr("Bon Appétit")},
		{"Mr. Food Test Kitchen", true, ptr("Mr. Food Test Kitchen")},
	} {
		src, err := dinners.SourceOf(sql.NullString{String: tc.stored, Valid: tc.valid}, link)
		if err != nil {
			t.Fatalf("SourceOf(%q): %v", tc.stored, err)
		}
		if got := src.SiteName; (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("SourceOf(%q) site name = %v, want %v", tc.stored, deref(got), deref(tc.want))
		}
	}
	// A link that does not parse is an error with or without a site name.
	for _, site := range []sql.NullString{{String: "Delish", Valid: true}, {}} {
		if _, err := dinners.SourceOf(site, "https://secret-token\x7f.example/%zz"); err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("SourceOf(%v) of an unparseable link: err = %v, want one not quoting the link", site, err)
		}
	}
}

// TestSafeSourceURLOnlyWithoutSecrets: a recipe link is shown only when it
// is https to a host with no user, password, query or fragment, each of
// which can carry a token; any other link, or one that does not parse, is
// withheld whole.
func TestSafeSourceURLOnlyWithoutSecrets(t *testing.T) {
	t.Parallel()
	for _, link := range []string{
		"https://www.example.com/recipes/chicken-curry",
		"https://example.com",
		"https://example.com:8443/stew/",
		"https://[2001:db8::1]/curry",
		"https://1.2.3.4/curry",
		"https://Kitchen-2.Example.com./curry",
	} {
		if got := dinners.SafeSourceURL(link); got == nil || *got != link {
			t.Errorf("SafeSourceURL(%q) = %v, want the link", link, deref(got))
		}
	}
	for _, link := range []string{
		"https://example.com/curry?access_token=query-token",
		"https://example.com/curry?",
		"https://user:password@example.com/curry",
		"https://token@example.com/curry",
		"https://@example.com/curry",
		"http://example.com/curry",
		"https://example.com/curry#token=fragment-token",
		"https://example.com/curry#",
		"ftp://example.com/curry",
		"https:example.com/curry",
		"https:///curry",
		"https://example.com:99999/curry",
		"https://example.com:0/curry",
		"//example.com/curry",
		"https://secret-token\x7f.example/%zz",
		"",
		// Hosts url.Parse accepts but the browser's URL parser refuses
		// (or reads as another host), which would fail the whole recipe
		// screen.
		"https://[bad]/curry",
		"https://[fe80::1%25en0]/curry",
		"https://[1.2.3.4]/curry",
		"https://1.2.3.999/curry",
		"https://1.2.3.4.5/curry",
		"https://999/curry",
		"https://example.0x/curry",
		"https://xn--a/curry",
		"https://ex%61mple.com/curry",
		"https://ex%2fample.com/curry",
		"https://a..b/curry",
		"https://café.example/curry",
	} {
		if got := dinners.SafeSourceURL(link); got != nil {
			t.Errorf("SafeSourceURL(%q) = %q, want nil", link, *got)
		}
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
