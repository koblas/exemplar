package gates

import (
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// The auto-approve path denylist. It is hardcoded and
// CODEOWNERS-owned by Security: config may add patterns
// (auto_approve.extra_denylist) but can never remove one. Matching is
// case-insensitive, so Makefile and makefile are both denied.

// deniedPaths are anchored at the repo root.
var deniedPaths = []string{
	".github/**", // workflows, rulesets, instructions, copilot-instructions.md
	".gitlab-ci.yml", ".buildkite/**", ".circleci/**",
	".cursor/rules/**",
	// Run on a developer's machine or in their container: dev containers
	// and git hooks.
	".devcontainer/**", ".husky/**",
	// Exemplar's own review configuration in the target repo. Added
	// because a merged change to these alters what
	// later reviews check.
	".exemplar/**", ".exemplarignore", ".gitattributes", ".gitleaksignore",
}

// deniedNames match a file's base name in any directory.
var deniedNames = []string{
	"codeowners",
	// Checkout behaviour in any directory: a nested .gitattributes can mark
	// source binary, which takes it out of review.
	".gitattributes", ".gitmodules",
	"jenkinsfile",
	"build", "build.bazel", "*.bzl", "workspace", "module.bazel", "makefile", "cmakelists.txt",
	"go.mod", "go.sum", "package.json", "*lock*", "requirements*.txt", "pyproject.toml", "cargo.*", "pom.xml", "build.gradle*",
	// Other files that change what is built, installed or run.
	"dockerfile", "dockerfile.*", "*.dockerfile", "containerfile", "docker-compose*.yml", "docker-compose*.yaml",
	"compose.yml", "compose.yaml", "setup.py", "setup.cfg", "gemfile", "*.gemspec", "rakefile", ".npmrc", ".yarnrc*",
	".pre-commit-config.yaml", "justfile", "taskfile.yml", "taskfile.yaml", "build.sbt", "meson.build",
	// Generated instruction files, which rule exports write.
	"agents.md", "claude.md",
}

// ConfigRepoName is the config repo's name; every path in it is denied.
const ConfigRepoName = "exemplar-config"

// Denylisted reports whether p may never be auto-approved in repo, by the
// hardcoded list or a repo's extra patterns (matched as written).
func Denylisted(repo, p string, extra []string) bool {
	if _, name, _ := strings.Cut(repo, "/"); strings.EqualFold(name, ConfigRepoName) {
		return true
	}
	lp := strings.ToLower(p)
	for _, pat := range deniedPaths {
		if doublestar.MatchUnvalidated(pat, lp) {
			return true
		}
	}
	base := path.Base(lp)
	for _, pat := range deniedNames {
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
	}
	for _, pat := range extra {
		if doublestar.MatchUnvalidated(pat, p) {
			return true
		}
	}
	return false
}
