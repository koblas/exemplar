-- Who may read what an evaluation reads: mode eval runs a config head's
-- cases on other repos, and each case repo must be one the requester
-- could read. NULL is a GitHub login, checked against the repo's
-- collaborator permission; a list is a service's or OIDC person's
-- evaluate:<owner>/<repo> permissions (svcauth.Claims.CanEvaluate).

-- +goose Up
ALTER TABLE evaluations ADD COLUMN requester_scopes text[];

-- +goose Down
ALTER TABLE evaluations DROP COLUMN requester_scopes;
