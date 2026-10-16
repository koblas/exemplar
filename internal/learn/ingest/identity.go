package ingest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Pseudonymizer maps GitHub logins to stable HMAC-SHA256 pseudonyms.
// Logins are never stored; ranking counts distinct pseudonyms.
type Pseudonymizer struct{ key []byte }

// NewPseudonymizer refuses keys shorter than 32 bytes.
func NewPseudonymizer(key []byte) (*Pseudonymizer, error) {
	if len(key) < 32 {
		return nil, errors.New("ingest: pseudonym key must be at least 32 bytes")
	}
	return &Pseudonymizer{key: slices.Clone(key)}, nil
}

// PID returns login's pseudonym. Logins are case-insensitive on GitHub.
func (p *Pseudonymizer) PID(login string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(strings.ToLower(login)))
	return hex.EncodeToString(m.Sum(nil))
}

var urlRE = regexp.MustCompile(`https?://[^\s<>()\[\]"'` + "`" + `]+`)

// DocURLs returns the normalized URLs in body whose host is on the docs
// allowlist. A host entry matches itself and its subdomains; an entry with
// a path ("github.com/acme/style-guide") also requires that path prefix.
func DocURLs(body string, allow []string) []string {
	out := []string{}
	for _, raw := range urlRE.FindAllString(body, -1) {
		raw = strings.TrimRight(raw, ".,;:!?")
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		u.Scheme = "https"
		u.Host = strings.ToLower(u.Host)
		u.RawQuery, u.Fragment, u.User = "", "", nil
		u.Path = strings.TrimSuffix(u.Path, "/")
		norm := u.String()
		if allowed(u, allow) && !slices.Contains(out, norm) {
			out = append(out, norm)
		}
	}
	return out
}

func allowed(u *url.URL, allow []string) bool {
	for _, a := range allow {
		host, prefix, _ := strings.Cut(strings.ToLower(a), "/")
		if u.Host != host && !strings.HasSuffix(u.Host, "."+host) {
			continue
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(u.Path)+"/", "/"+strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}
