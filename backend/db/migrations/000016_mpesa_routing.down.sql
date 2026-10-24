ALTER TABLE mpesa_stk_requests
  DROP FOREIGN KEY fk_stk_shortcode,
  DROP KEY idx_stk_shortcode,
  DROP COLUMN shortcode_id;

ALTER TABLE mpesa_transactions
  DROP FOREIGN KEY fk_mpesa_lease,
  DROP FOREIGN KEY fk_mpesa_shortcode,
  DROP KEY idx_mpesa_org_lease,
  DROP KEY idx_mpesa_shortcode,
  DROP COLUMN lease_id,
  DROP COLUMN shortcode_id;

DROP TABLE IF EXISTS mpesa_account_refs;
DROP TABLE IF EXISTS mpesa_shortcodes;
