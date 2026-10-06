package dinners

import (
	"net/url"
	"testing"
)

func TestNameFromURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		link string
		want string // "" means no usable name
	}{
		{"https://example.com/recipes/12345/chicken-tikka-masala/", "Chicken tikka masala"},
		{"https://example.com/recipe/beef_stew.html", "Beef stew"},
		{"https://example.com/2024/05/best-ever-chili-123", "Best ever chili"},
		{"https://example.com/crème-brûlée", "Crème brûlée"},
		{"https://example.com/recipes/chili?utm=1#notes", "Chili"},
		{"https://example.com/", ""},
		{"https://example.com/recipe/98765", ""},
		{"https://example.com/the-best-easy-recipe", ""},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.link)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := nameFromURL(u)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("nameFromURL(%s) = %q, %v; want %q", tt.link, got, ok, tt.want)
		}
	}
}
