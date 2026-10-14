-- name: GetIdentityLink :one
SELECT github_login, github_id FROM identity_links WHERE issuer = $1 AND subject = $2;

-- name: GitHubAccountLinkedTo :one
-- The OIDC identity a GitHub account is connected to.
SELECT issuer, subject FROM identity_links WHERE github_id = $1;

-- name: SaveIdentityLink :exec
INSERT INTO identity_links (issuer, subject, email, github_id, github_login) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (issuer, subject) DO UPDATE SET email = excluded.email, github_id = excluded.github_id,
  github_login = excluded.github_login, linked_at = now();
