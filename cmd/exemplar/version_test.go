package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	t.Parallel()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil || !strings.Contains(out.String(), "exemplar version dev") {
		t.Errorf("--version = %q, %v", out.String(), err)
	}
}
