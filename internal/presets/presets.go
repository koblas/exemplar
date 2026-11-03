// Package presets embeds the rule presets that ship with the binary. A
// config names one in extends as exemplar:<dir>; internal/config loads and
// resolves them.
package presets

import "embed"

// FS holds one directory of rule files per preset. Every rule id starts
// with Prefix.
//
//go:embed recommended/*.yaml
var FS embed.FS

// Prefix is reserved for shipped preset rule ids; a config or target repo
// rule may not use it.
const Prefix = "exemplar."

// RefPrefix names a shipped preset in extends.
const RefPrefix = "exemplar:"
