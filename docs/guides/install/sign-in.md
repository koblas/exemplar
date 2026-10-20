# How to set up sign-in

People sign in to exemplar-api with GitHub OAuth, OpenID Connect, or both. The settings follow [Coder's](https://coder.com/docs/admin/users/oidc-auth) and live in the `exemplar-auth` ConfigMap, which the API reads through `envFrom`. Every variable is described on [Environment variables](../../reference/environment-variables.md#exemplar-api).

## GitHub OAuth

It is on once `exemplar-github-app` exists (it holds the App's client id and secret). Register `https://<api>/oauth/callback` as the App's callback URL; the setup page does this for you.

- By default only members of the config repo's owner may sign in. Set `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` (comma-separated) to change that, or `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true` to open it to anyone.
- Each person sees the repos their GitHub permissions allow.
- For GitHub Enterprise Server, set `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL`.

## OpenID Connect

Works with Okta, Auth0, Azure AD, Keycloak and any other OIDC provider.

1. Register a confidential client with `https://<api-host>/oidc/callback` as its redirect URI.
2. Set `EXEMPLAR_OIDC_ISSUER_URL` and `EXEMPLAR_OIDC_CLIENT_ID` in `exemplar-auth`, and create the `exemplar-oidc` Secret with `client-secret`.
3. Map groups to repositories with `EXEMPLAR_OIDC_GROUP_REPOS`, for example `{"engineering": ["acme/*"]}`. Repo access for OIDC users comes **only** from this map.
4. Restrict who may sign in with `EXEMPLAR_OIDC_EMAIL_DOMAIN` and `EXEMPLAR_OIDC_ALLOWED_GROUPS`.
5. For `exemplar login --oidc`, add a public client with the device flow enabled and set it as `EXEMPLAR_OIDC_CLI_CLIENT_ID`.

Claim names (`EXEMPLAR_OIDC_EMAIL_FIELD`, `EXEMPLAR_OIDC_USERNAME_FIELD`, `EXEMPLAR_OIDC_GROUP_FIELD`), scopes, extra authorization parameters and the sign-in button's text and icon are in the reference.

## Both

Each OIDC user connects their GitHub account once after signing in, so feedback and rule ownership use their real GitHub login.

- `EXEMPLAR_OIDC_GITHUB_LINK` is `required` (the default), `optional` or `off`.
- With `optional` or `off`, someone who has not connected GitHub signs in as `oidc:<username>`. That login never matches a GitHub account, so they cannot run rule-owner actions, and their feedback counts as an observer's.
- `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false` keeps GitHub as the connect step only, with no GitHub sign-in button.
- People switch accounts at `/ui/connect-github`.

## Service callers

CI jobs and other services call `/v1/evaluate` with a JWT instead of a sign-in: an Auth0 access token for this API (set `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` in `exemplar-settings`; both empty turns service callers off) or a GitHub Actions OIDC token. A service needs an `evaluate:<owner>/<repo>` (or `<owner>/*`, `*`) permission for every repository it evaluates. [How to set up config CI](../rules/config-ci.md) has the Auth0 client config CI needs.
