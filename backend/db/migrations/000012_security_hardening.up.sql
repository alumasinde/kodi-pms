-- Security hardening: DB-backed account lock state, per-user token invalidation,
-- and refresh-token rotation/grace metadata.

ALTER TABLE users
  ADD COLUMN token_version         BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER status,
  ADD COLUMN failed_login_attempts INT UNSIGNED    NOT NULL DEFAULT 0 AFTER token_version,
  ADD COLUMN locked_until          TIMESTAMP       NULL DEFAULT NULL AFTER failed_login_attempts;

ALTER TABLE refresh_tokens
  ADD COLUMN rotated_at    TIMESTAMP NULL DEFAULT NULL AFTER revoked_at,
  ADD COLUMN grace_used_at TIMESTAMP NULL DEFAULT NULL AFTER rotated_at;
