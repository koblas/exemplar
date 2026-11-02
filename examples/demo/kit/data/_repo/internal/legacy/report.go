// Package legacy is the monthly report from before the payments rewrite. It
// is kept until finance stops using it and is not otherwise maintained.
package legacy

import "fmt"

// Line formats one report line.
func Line(name string, cents int64) string {
	return fmt.Sprintf("%-30s %10d", name, cents)
}
