-- The model that produced each extraction's embedding. The embed job
-- re-embeds rows whose model is not the gateway's providers.embed, and
-- cluster reads one model's vectors at a time, so a model change never
-- mixes vector spaces. Rows embedded before this column existed have NULL
-- and are embedded again.

-- +goose Up
ALTER TABLE extractions ADD COLUMN embedding_model text;

-- +goose Down
ALTER TABLE extractions DROP COLUMN embedding_model;
