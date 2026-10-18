package gh

import "testing"

func TestCloneURL(t *testing.T) {
	for _, tc := range []struct{ web, want string }{
		{"", "https://github.com/acme/api.git"},
		{"https://github.com/", "https://github.com/acme/api.git"},
		{"https://ghe.acme.com/", "https://ghe.acme.com/acme/api.git"},
		{"https://ghe.acme.com", "https://ghe.acme.com/acme/api.git"},
	} {
		if got := CloneURL(tc.web)("acme/api"); got != tc.want {
			t.Errorf("CloneURL(%q) = %q, want %q", tc.web, got, tc.want)
		}
	}
}
