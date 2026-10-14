package scope

import (
	"errors"
	"testing"
)

func TestPatternsMatch(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"recursive extension", []string{"**/*.go"}, "src/net/fetch.go", true},
		{"recursive extension at root", []string{"**/*.go"}, "main.go", true},
		{"other extension", []string{"**/*.go"}, "src/net/fetch.cpp", false},
		{"excluded tests", []string{"**/*.go", "!**/*_test.go"}, "src/net/fetch_test.go", false},
		{"exclude order does not matter", []string{"!**/*_test.go", "**/*.go"}, "src/net/fetch_test.go", false},
		{"not excluded", []string{"**/*.go", "!**/*_test.go"}, "src/net/fetch.go", true},
		{"directory prefix", []string{"src/auth/**"}, "src/auth/login/handler.go", true},
		{"directory prefix miss", []string{"src/auth/**"}, "src/authz/handler.go", false},
		{"brace alternation", []string{"**/*.{ts,tsx}"}, "web/app/page.tsx", true},
		{"single star stays in segment", []string{"src/*.go"}, "src/net/fetch.go", false},
		{"any of several includes", []string{"docs/**", "**/*.md"}, "README.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := CompilePatterns(tt.patterns)
			if err != nil {
				t.Fatalf("CompilePatterns(%q): %v", tt.patterns, err)
			}
			if got := p.Match(tt.path); got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCompilePatternsErrors(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		wantErr  error // nil means any error
	}{
		{"empty list", nil, ErrNoIncludePattern},
		{"only excludes", []string{"!**/*_test.go"}, ErrNoIncludePattern},
		{"empty pattern", []string{""}, nil},
		{"bare negation", []string{"**/*.go", "!"}, nil},
		{"unclosed class", []string{"src/[a-z.go"}, nil},
		{"unclosed brace", []string{"**/*.{go,ts"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CompilePatterns(tt.patterns)
			if err == nil {
				t.Fatalf("CompilePatterns(%q) succeeded, want error", tt.patterns)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("CompilePatterns(%q) = %v, want %v", tt.patterns, err, tt.wantErr)
			}
		})
	}
}

func TestPatternsFilter(t *testing.T) {
	p, err := CompilePatterns([]string{"**/*.go", "!**/*_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	got := p.Filter([]string{"a.go", "a_test.go", "b.md", "pkg/c.go"})
	if want := []string{"a.go", "pkg/c.go"}; !equalStrings(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}
	if got := p.Filter(nil); len(got) != 0 {
		t.Fatalf("Filter(nil) = %v, want empty", got)
	}
}
