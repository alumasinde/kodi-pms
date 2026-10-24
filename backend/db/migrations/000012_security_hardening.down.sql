ALTER TABLE refresh_tokens
  DROP COLUMN grace_used_at,
  DROP COLUMN rotated_at;

ALTER TABLE users
  DROP COLUMN locked_until,
  DROP COLUMN failed_login_attempts,
  DROP COLUMN token_version;
