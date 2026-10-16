package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/koblas/exemplar/internal/scope"
)

// fingerprinter computes stable finding identities (spec §6.2):
//
//	sha256(rule_id | path | normalized code at anchor ±2 lines | k)
//
// where k is the ordinal of this anchor among anchors in the same file and
// rule with identical normalized code, so two copies of the same loop are
// two findings, not one dropped as a duplicate.
type fingerprinter struct {
	ws    *Workspace
	files map[string][]string // rev:path -> lines
	seen  map[string]int      // rule|path|code -> next k
}

func newFingerprinter(ws *Workspace) *fingerprinter {
	return &fingerprinter{ws: ws, files: map[string][]string{}, seen: map[string]int{}}
}

func (fp *fingerprinter) lines(ctx context.Context, side scope.Side, path string) []string {
	rev := fp.ws.HeadSHA
	if side == scope.SideLeft {
		rev = fp.ws.BaseSHA
	}
	key := rev + ":" + path
	if l, ok := fp.files[key]; ok {
		return l
	}
	data, err := fp.ws.Show(ctx, rev, path)
	var l []string
	if err == nil {
		l = strings.Split(string(data), "\n")
	}
	fp.files[key] = l
	return l
}

// normalizedCode returns the anchor's code with two lines of context,
// whitespace-normalized so reformatting does not change identity.
func (fp *fingerprinter) normalizedCode(ctx context.Context, a scope.Anchor) string {
	lines := fp.lines(ctx, a.Side, a.Path)
	lo, hi := max(1, a.StartLine-2), min(len(lines), a.EndLine+2)
	var b strings.Builder
	for i := lo; i <= hi; i++ {
		b.WriteString(normalizeSpace(lines[i-1]))
		b.WriteByte('\n')
	}
	return b.String()
}

// next returns the fingerprint for a new occurrence.
func (fp *fingerprinter) next(ctx context.Context, ruleID string, a scope.Anchor) string {
	id, _, _ := fp.identify(ctx, ruleID, a)
	return id
}

// identify returns a new occurrence's fingerprint, the normalized code it
// was computed from and its ordinal k, which re-anchoring after a rebase
// searches for (FR-C4).
func (fp *fingerprinter) identify(ctx context.Context, ruleID string, a scope.Anchor) (string, string, int) {
	code := fp.normalizedCode(ctx, a)
	base := ruleID + "|" + a.Path + "|" + code
	k := fp.seen[base]
	fp.seen[base] = k + 1
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d", base, k))
	return hex.EncodeToString(sum[:]), code, k
}
