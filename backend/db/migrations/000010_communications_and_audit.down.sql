DROP TRIGGER IF EXISTS trg_pii_access_no_delete;
DROP TRIGGER IF EXISTS trg_pii_access_no_update;
DROP TRIGGER IF EXISTS trg_audit_logs_no_delete;
DROP TRIGGER IF EXISTS trg_audit_logs_no_update;
DROP TRIGGER IF EXISTS trg_messages_org_upd;
DROP TRIGGER IF EXISTS trg_messages_org_ins;

DROP TABLE IF EXISTS pii_access_logs;
DROP TABLE IF EXISTS pii_retention_policies;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS reviews;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS notifications;
