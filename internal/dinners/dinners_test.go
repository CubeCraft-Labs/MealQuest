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
		{"https://example.com/best-chili-v2", "Best chili"},
		{"https://example.com/stew-access_token-personaltokenabc123", "Stew"},
		{"https://example.com/stew-token-abcdefghijk", "Stew"},
		{"https://example.com/chili-api-key-zyxwvut", "Chili"},
		{"https://example.com/stew-x7k2p9q4", "Stew"},
		{"https://example.com/recipes/token-abcdefghijk", ""},
		{"https://example.com/key-lime-pie", "Key lime pie"},
		{"https://example.com/stew-t=abcdef", "Stew"},
		{"https://example.com/stew-accesstoken-abcdefghijk", "Stew"},
		{"https://example.com/stew-abcdefghijklmnopqrstuvwxyz", "Stew"},
		{"https://example.com/cre%CC%80me-bru%CC%82le%CC%81e", "Cre\u0300me bru\u0302le\u0301e"},
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
