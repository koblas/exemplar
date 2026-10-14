-- GitHub accounts connected to OIDC sign-ins (spec §12.8): after an OIDC
-- sign-in the person connects GitHub once, so Exemplar can compare them
-- with GitHub logins and teams (feedback roles, rule-owner checks). Repo
-- access still comes from their OIDC groups. One GitHub account belongs
-- to one OIDC identity.

-- +goose Up
CREATE TABLE identity_links (
  issuer       text   NOT NULL,
  subject      text   NOT NULL,
  email        text   NOT NULL,
  github_id    bigint NOT NULL UNIQUE,
  github_login text   NOT NULL,
  linked_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (issuer, subject)
);

-- +goose Down
DROP TABLE identity_links;
