package secureurl

import (
	"net/url"
	"testing"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw string
		ok  bool
	}{
		{"https://meals.example.com", true},
		{"https://auth.example.com/application/o/mealquest/", true},
		{"https://192.0.2.10:8443", true},
		{"http://localhost:8080", true},
		{"http://LOCALHOST:8080", true},
		{"http://127.0.0.1:8080/path", true},
		{"http://[::1]:8080", true},
		{"http://[::1]", true},
		{"http://meals.example.com", false},
		{"http://192.0.2.10", false},
		{"http://127.0.0.2", false},
		{"http://localhost.example.com", false},
		{"http://0.0.0.0:8080", false},
		{"ftp://localhost", false},
		{"https://", false},
		{"meals.example.com", false},
	}
	for _, tc := range tests {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(u); (err == nil) != tc.ok {
			t.Errorf("Check(%q) error = %v, want ok=%v", tc.raw, err, tc.ok)
		}
	}
}
