# Sign-in

People sign in to exemplar-api with GitHub OAuth, OpenID Connect, or both. The settings follow [Coder's](https://coder.com/docs/admin/users/oidc-auth) and live in the `exemplar-auth` ConfigMap.

- **GitHub OAuth** is on once `exemplar-github-app` exists (it holds the App's client id and secret).
  - By default only members of the config repo's owner may sign in. Set `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` (comma-separated) to change that, or `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true`.
  - Each person sees the repos their GitHub permissions allow.
  - For GitHub Enterprise Server, set `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL`.
- **OpenID Connect** (Okta, Auth0, Azure AD, Keycloak, …).
  - Set `EXEMPLAR_OIDC_ISSUER_URL` and `EXEMPLAR_OIDC_CLIENT_ID`, and create the `exemplar-oidc` Secret.
  - Register `https://<api-host>/oidc/callback` as the redirect URI.
  - Repo access comes **only** from `EXEMPLAR_OIDC_GROUP_REPOS`, which maps IdP groups to repos, for example `{"engineering": ["acme/*"]}`.
  - Restrict sign-in with `EXEMPLAR_OIDC_EMAIL_DOMAIN` and `EXEMPLAR_OIDC_ALLOWED_GROUPS`.
  - For `exemplar login --oidc`, add a public client with the device flow enabled and set it as `EXEMPLAR_OIDC_CLI_CLIENT_ID`.
  - The full list of settings is in [`internal/oidc`](https://github.com/koblas/exemplar/blob/main/internal/oidc/oidc.go).
- **Both.**
  - Each OIDC user connects their GitHub account once after signing in, so feedback and rule ownership use their real GitHub login.
  - `EXEMPLAR_OIDC_GITHUB_LINK` is `required` (the default), `optional` or `off`.
  - `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false` keeps GitHub as the connect step only, with no GitHub sign-in button.
