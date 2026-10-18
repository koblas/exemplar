package envcfg

import "testing"

func TestPublicURLs(t *testing.T) {
	tests := []struct {
		name, api, webhook string
		want               URLs
		bad                bool
	}{
		{
			name: "one host", api: "https://exemplar.acme.com/",
			want: URLs{API: "https://exemplar.acme.com", Webhook: "https://exemplar.acme.com/webhook", Callback: "https://exemplar.acme.com/oauth/callback"},
		},
		{
			name: "webhook host", api: "https://exemplar.acme.com", webhook: "https://hooks.acme.com",
			want: URLs{API: "https://exemplar.acme.com", Webhook: "https://hooks.acme.com/webhook", Callback: "https://exemplar.acme.com/oauth/callback"},
		},
		{
			name: "webhook with path", api: "https://exemplar.acme.com", webhook: "https://hooks.acme.com/webhook",
			want: URLs{API: "https://exemplar.acme.com", Webhook: "https://hooks.acme.com/webhook", Callback: "https://exemplar.acme.com/oauth/callback"},
		},
		{name: "unset", bad: true},
		{name: "http", api: "http://exemplar.acme.com", bad: true},
		{name: "bad webhook", api: "https://exemplar.acme.com", webhook: "hooks.acme.com", bad: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"EXEMPLAR_API_URL": tc.api, "EXEMPLAR_WEBHOOK_URL": tc.webhook}
			got, err := PublicURLs(func(k string) string { return env[k] })
			if (err != nil) != tc.bad {
				t.Fatalf("PublicURLs = %+v, %v", got, err)
			}
			if !tc.bad && got != tc.want {
				t.Fatalf("PublicURLs = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOwner(t *testing.T) {
	if Owner("acme/exemplar-config") != "acme" || Owner("nope") != "" {
		t.Fatal("Owner")
	}
}
