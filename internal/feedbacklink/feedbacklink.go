// Package feedbacklink signs and verifies feedback links:
// /f/{finding}/{reason}?s=<HMAC(finding, reason)>. The HMAC prevents
// forged links; who clicked comes from login, never from the link.
package feedbacklink

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Reasons are the link reasons, in footer order, with their labels.
var Reasons = []struct{ Reason, Label, Kind string }{
	{"fixed", "Fixed", "fb_fixed"},
	{"wrong", "Wrong", "fb_wrong"},
	{"not_important", "Not important here", "fb_not_important"},
	{"handled", "Already handled", "fb_handled"},
	{"disagree", "Disagree with rule", "fb_disagree"},
}

// Kind is a reason's feedback_events kind ("" for an unknown reason).
func Kind(reason string) string {
	i := slices.IndexFunc(Reasons, func(r struct{ Reason, Label, Kind string }) bool { return r.Reason == reason })
	if i < 0 {
		return ""
	}
	return Reasons[i].Kind
}

// MinKeyBytes is the shortest accepted key.
const MinKeyBytes = 32

// Signer signs links for one public base URL.
type Signer struct {
	key  []byte
	base string
}

// New returns a Signer for links under base (exemplar-api's public URL).
func New(key []byte, base string) (*Signer, error) {
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("feedbacklink: key must be at least %d bytes", MinKeyBytes)
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://localhost") && !strings.HasPrefix(base, "http://127.0.0.1") {
		return nil, fmt.Errorf("feedbacklink: base %q must be https", base)
	}
	return &Signer{key: key, base: strings.TrimSuffix(base, "/")}, nil
}

func (s *Signer) mac(finding int64, reason string) []byte {
	m := hmac.New(sha256.New, s.key)
	fmt.Fprintf(m, "exemplar-feedback-link/v1\x00%d\x00%s", finding, reason)
	return m.Sum(nil)
}

// Sig is the link signature for (finding, reason).
func (s *Signer) Sig(finding int64, reason string) string {
	return base64.RawURLEncoding.EncodeToString(s.mac(finding, reason))
}

// URL is the signed link.
func (s *Signer) URL(finding int64, reason string) string {
	return fmt.Sprintf("%s/f/%d/%s?s=%s", s.base, finding, reason, s.Sig(finding, reason))
}

// ErrBadLink is returned for a link that was not signed by this key.
var ErrBadLink = errors.New("feedbacklink: invalid link")

// Verify checks a link's parts, in constant time.
func (s *Signer) Verify(finding, reason, sig string) (int64, error) {
	id, err := strconv.ParseInt(finding, 10, 64)
	if err != nil || id < 1 || Kind(reason) == "" {
		return 0, ErrBadLink
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(id, reason)) {
		return 0, ErrBadLink
	}
	return id, nil
}

// Footer is the comment's feedback line: one link per reason.
func (s *Signer) Footer(finding int64) string {
	parts := make([]string, len(Reasons))
	for i, r := range Reasons {
		parts[i] = fmt.Sprintf("[%s](%s)", r.Label, s.URL(finding, r.Reason))
	}
	return strings.Join(parts, " · ")
}
