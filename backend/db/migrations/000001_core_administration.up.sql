-- Multi-tenancy foundation: organizations, their domains, and per-org settings.
CREATE TABLE IF NOT EXISTS organizations (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  org_name          VARCHAR(150) NOT NULL,
  slug          VARCHAR(100) NOT NULL,
  legal_name    VARCHAR(200) NULL,
  kra_pin       VARCHAR(20)  NULL,
  email         VARCHAR(255) NULL,
  phone         VARCHAR(20)  NULL,
  org_address       VARCHAR(255) NULL,
  county        VARCHAR(100) NULL,
  country_code  CHAR(2)      NOT NULL DEFAULT 'KE',
  currency_code CHAR(3)      NOT NULL DEFAULT 'KES',
  timezone      VARCHAR(50)  NOT NULL DEFAULT 'Africa/Nairobi',
  org_status        VARCHAR(30)  NOT NULL DEFAULT 'active',
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at    TIMESTAMP NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_organizations_slug (slug),
  CONSTRAINT chk_organizations_status CHECK (org_status IN ('active','suspended','closed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS organization_domains (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  domain          VARCHAR(255) NOT NULL,
  is_primary      TINYINT(1) NOT NULL DEFAULT 0,
  verified_at     TIMESTAMP NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_org_domains_domain (domain),
  KEY idx_org_domains_org (organization_id),
  CONSTRAINT fk_org_domains_org FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- organization_id NULL = global default; a row with an org id overrides it.
-- org_scope makes (scope, key) unique even for the NULL/global case.
CREATE TABLE IF NOT EXISTS system_settings (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NULL,
  org_scope       BIGINT UNSIGNED GENERATED ALWAYS AS (IFNULL(organization_id, 0)) STORED,
  setting_key     VARCHAR(100) NOT NULL,
  setting_value   TEXT NULL,
  value_type      VARCHAR(20) NOT NULL DEFAULT 'string',
  description     VARCHAR(255) NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_settings_scope_key (org_scope, setting_key),
  CONSTRAINT fk_settings_org FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
  CONSTRAINT chk_settings_type CHECK (value_type IN ('string','int','decimal','bool','json'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
