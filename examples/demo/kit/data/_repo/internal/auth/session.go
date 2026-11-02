// Package auth checks Ledger's session cookies.
package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
)

// ErrNoSession is returned when a request carries no valid session.
var ErrNoSession = errors.New("no valid session")

// Check reports whether r carries the expected session token.
func Check(r *http.Request, want string) error {
	c, err := r.Cookie("ledger_session")
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(want)) != 1 {
		return ErrNoSession
	}
	return nil
}
