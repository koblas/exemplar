-- A pending comment from a repo that is no longer eligible is parked as
-- 'ineligible' instead of failed, and goes back to 'pending' once the
-- repo is eligible again. Rows the earlier code failed for that reason
-- are parked too.

-- +goose Up
ALTER TABLE mined_comments DROP CONSTRAINT mined_comments_extract_status_check;
ALTER TABLE mined_comments ADD CONSTRAINT mined_comments_extract_status_check
  CHECK (extract_status IN ('pending', 'in_batch', 'accepted', 'rejected', 'error', 'ineligible'));
UPDATE mined_comments SET extract_status = 'ineligible', extract_error = NULL
WHERE extract_status = 'error' AND extract_error = 'repo is not eligible';

-- +goose Down
UPDATE mined_comments SET extract_status = 'error', extract_error = 'repo is not eligible'
WHERE extract_status = 'ineligible';
ALTER TABLE mined_comments DROP CONSTRAINT mined_comments_extract_status_check;
ALTER TABLE mined_comments ADD CONSTRAINT mined_comments_extract_status_check
  CHECK (extract_status IN ('pending', 'in_batch', 'accepted', 'rejected', 'error'));
