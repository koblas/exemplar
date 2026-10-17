package main

import (
	"errors"
	"testing"
)

func TestSkipExport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		appID   string
		repos   []string
		skip    bool
		wantErr bool
	}{
		{name: "no app, no exports", appID: "", skip: true},
		{name: "app 0, no exports", appID: "0", skip: true},
		{name: "no app, exports configured", appID: "0", repos: []string{"acme/api"}, wantErr: true},
		{name: "app set, no exports", appID: "42"},
		{name: "app set, exports configured", appID: "42", repos: []string{"acme/api"}},
		{name: "app id malformed is left to the app", appID: "x1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skip, err := skipExport(tc.appID, tc.repos)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, errRequired) {
				t.Errorf("err = %v, want errRequired", err)
			}
			if skip != tc.skip {
				t.Errorf("skip = %v, want %v", skip, tc.skip)
			}
		})
	}
}
