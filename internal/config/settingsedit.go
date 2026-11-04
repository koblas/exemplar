package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/koblas/exemplar/internal/schema"
)

// ErrSettingsLayout reports a global.yaml whose rule_settings
// EditRuleSetting cannot change in place.
var ErrSettingsLayout = errors.New("rule_settings layout not supported for automatic edits")

var settingsKey = regexp.MustCompile(`(?m)^rule_settings:[ \t]*(.*)$`)

// EditRuleSetting returns global.yaml with rule_settings.<id> changed by
// fn, leaving every other line byte for byte. The entry is written as one
// flow-style line, so comments inside or after an existing entry for id
// are dropped. The result is decoded and compared with the input, so an
// edit that would change anything but that entry is refused.
func EditRuleSetting(global []byte, id string, fn func(*schema.RuleSetting)) ([]byte, error) {
	var before schema.GlobalConfig
	if err := schema.Decode(schema.DocGlobal, global, &before); err != nil {
		return nil, fmt.Errorf("decode global.yaml: %w", err)
	}
	set := before.RuleSettings[id]
	fn(&set)
	entry := "  " + id + ": " + flowSetting(set)

	out, err := spliceSetting(global, id, entry)
	if err != nil {
		return nil, err
	}

	var after schema.GlobalConfig
	if err := schema.Decode(schema.DocGlobal, out, &after); err != nil {
		return nil, fmt.Errorf("edited global.yaml does not decode: %w", err)
	}
	if !reflect.DeepEqual(after.RuleSettings[id], set) {
		return nil, fmt.Errorf("%w: rule_settings.%s did not take the new value", ErrSettingsLayout, id)
	}
	want := before
	want.RuleSettings = maps.Clone(before.RuleSettings)
	if want.RuleSettings == nil {
		want.RuleSettings = map[string]schema.RuleSetting{}
	}
	want.RuleSettings[id] = set
	if !sameJSON(want, after) {
		return nil, fmt.Errorf("%w: the edit changed more than rule_settings.%s", ErrSettingsLayout, id)
	}
	return out, nil
}

func spliceSetting(global []byte, id, entry string) ([]byte, error) {
	text := string(global)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	loc := settingsKey.FindStringSubmatchIndex(text)
	if loc == nil {
		return []byte(text + "rule_settings:\n" + entry + "\n"), nil
	}
	switch strings.TrimSpace(stripComment(text[loc[2]:loc[3]])) {
	case "":
	case "{}":
		return []byte(text[:loc[0]] + "rule_settings:\n" + entry + text[loc[1]:]), nil
	default:
		return nil, fmt.Errorf("%w: rule_settings is written inline", ErrSettingsLayout)
	}

	// The block is the lines after the key that are indented, blank or
	// comments; it ends at the next top-level key. last is the end of its
	// last mapping line, where a new entry goes.
	start := loc[1] + 1
	last, childIndent := 0, -1
	entryStart, entryEnd := -1, -1
	pos := 0
	for _, line := range strings.SplitAfter(text[start:], "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		body := strings.TrimSpace(trimmed)
		next := pos + len(line)
		if body == "" || strings.HasPrefix(body, "#") {
			pos = next
			continue
		}
		if indent == 0 {
			break
		}
		if childIndent < 0 {
			childIndent = indent
		}
		if indent == childIndent {
			if entryStart >= 0 && entryEnd < 0 {
				entryEnd = pos
			}
			if isKey(body, id) {
				entryStart = pos
			}
		}
		last, pos = next, next
	}
	if entryStart >= 0 && entryEnd < 0 {
		entryEnd = last
	}
	if childIndent < 0 {
		childIndent = 2
	}
	entry = strings.Repeat(" ", childIndent) + strings.TrimLeft(entry, " ") + "\n"
	if entryStart >= 0 {
		return []byte(text[:start+entryStart] + entry + text[start+entryEnd:]), nil
	}
	return []byte(text[:start+last] + entry + text[start+last:]), nil
}

// isKey reports whether a mapping line's key is id, quoted or not.
func isKey(body, id string) bool {
	for _, k := range []string{id, strconv.Quote(id), "'" + id + "'"} {
		if strings.HasPrefix(body, k+":") {
			return true
		}
	}
	return false
}

func stripComment(s string) string {
	if before, _, found := strings.Cut(s, " #"); found {
		return before
	}
	if strings.HasPrefix(strings.TrimSpace(s), "#") {
		return ""
	}
	return s
}

// flowSetting renders a setting as a YAML flow mapping, fields in schema
// order.
func flowSetting(s schema.RuleSetting) string {
	var parts []string
	if s.Status != "" {
		parts = append(parts, "status: "+s.Status)
	}
	if len(s.ExcludePaths) > 0 {
		q := make([]string, len(s.ExcludePaths))
		for i, p := range s.ExcludePaths {
			q[i] = strconv.Quote(p)
		}
		parts = append(parts, "exclude_paths: ["+strings.Join(q, ", ")+"]")
	}
	if s.Required != nil {
		parts = append(parts, "required: "+strconv.FormatBool(*s.Required))
	}
	if s.Owner != "" {
		parts = append(parts, "owner: "+strconv.Quote(s.Owner))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
