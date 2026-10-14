package playground

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/redact"
)

// zeroWidthAt is "@" followed by a zero-width space (U+200B): it reads as
// "@" but GitHub does not treat it as a mention.
var zeroWidthAt = "@" + string(rune(0x200b))

const (
	maxMessage      = 300
	maxListed       = 30
	maxInlineCode   = 40
	withheldMessage = "(message withheld: it may contain a secret)"
)

var (
	fenceRE  = regexp.MustCompile("(?s)```.*?(```|$)")
	inlineRE = regexp.MustCompile("`([^`]*)`")
	spaceRE  = regexp.MustCompile(`\s+`)
)

// SafeMessage makes a finding message fit for exemplar-config, which more
// people can read than the target repo: code blocks and long inline code
// are removed (short identifiers stay), secrets are redacted, @mentions
// are defused so the reply pings no one, and the text is capped.
func SafeMessage(sc *redact.Scanner, msg string) string {
	msg = fenceRE.ReplaceAllString(msg, " (code omitted) ")
	msg = inlineRE.ReplaceAllStringFunc(msg, func(s string) string {
		inner := strings.Trim(s, "`")
		if len(inner) > maxInlineCode || strings.ContainsAny(inner, " \t;{}()=") {
			return "(code omitted)"
		}
		return "`" + inner + "`"
	})
	msg = strings.TrimSpace(spaceRE.ReplaceAllString(msg, " "))
	msg = strings.ReplaceAll(msg, "@", zeroWidthAt)
	if utf8.RuneCountInString(msg) > maxMessage {
		msg = string([]rune(msg)[:maxMessage]) + "…"
	}
	if sc != nil {
		red, _, err := sc.Redact(msg)
		if err != nil {
			return withheldMessage
		}
		msg = red
	}
	return msg
}

// Format renders the reply: paths, lines and messages only.
func Format(t Target, headSHA string, ruleIDs []string, res *engine.Result, out Outcome) string {
	var b strings.Builder
	short := headSHA
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Fprintf(&b, "**Exemplar try** of %s on %s at `%s`\n\n", ruleList(ruleIDs), t, short)
	kept, dropped := 0, 0
	for _, f := range out.Findings {
		if f.Kept {
			kept++
		} else {
			dropped++
		}
	}
	fmt.Fprintf(&b, "%d %s would be reported, %d dropped by verification or validation · cost $%.4f\n",
		kept, plural(kept, "finding", "findings"), dropped, out.CostUSD)
	for _, run := range res.Runs {
		if slices.Contains(ruleIDs, run.RuleID) && run.Status != engine.RunOK {
			fmt.Fprintf(&b, "\n`%s` did not finish: %s", run.RuleID, run.Status)
			if run.Detail != "" {
				fmt.Fprintf(&b, " (%s)", SafeMessage(nil, run.Detail))
			}
			b.WriteString(". Findings may be missing.\n")
		}
	}
	section := func(title string, want bool) {
		n := 0
		for _, f := range out.Findings {
			if f.Kept != want {
				continue
			}
			if n == 0 {
				fmt.Fprintf(&b, "\n**%s**\n\n", title)
			}
			if n == maxListed {
				b.WriteString("- …\n")
				break
			}
			n++
			lines := fmt.Sprintf("L%d", f.Start)
			if f.End > f.Start {
				lines = fmt.Sprintf("L%d–%d", f.Start, f.End)
			}
			fmt.Fprintf(&b, "- `%s` %s", strings.ReplaceAll(f.Path, "`", "'"), lines)
			if len(ruleIDs) > 1 {
				fmt.Fprintf(&b, " (`%s`)", f.Rule)
			}
			if want {
				fmt.Fprintf(&b, ": %s\n", f.Message)
			} else {
				fmt.Fprintf(&b, ": dropped (%s)\n", f.Reason)
			}
		}
	}
	section("Would be reported", true)
	section("Dropped", false)
	b.WriteString("\nThe draft ran in isolation and posted nothing on the target PR. Code is never quoted here, because more people can read exemplar-config than most repos.\n")
	return b.String()
}
