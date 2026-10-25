package main

import "testing"

func TestRunnerDatabaseURL(t *testing.T) {
	const direct, pooled = "postgres://db/exemplar", "postgres://pooler/exemplar"
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"pooled", map[string]string{"EXEMPLAR_DATABASE_URL": direct, "EXEMPLAR_RUNNER_DATABASE_URL": pooled}, pooled},
		{"direct without a pooler", map[string]string{"EXEMPLAR_DATABASE_URL": direct}, direct},
		{"empty pooler is unset", map[string]string{"EXEMPLAR_DATABASE_URL": direct, "EXEMPLAR_RUNNER_DATABASE_URL": ""}, direct},
		{"neither", map[string]string{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runnerDatabaseURL(func(k string) string { return tc.env[k] }); got != tc.want {
				t.Errorf("runnerDatabaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}
