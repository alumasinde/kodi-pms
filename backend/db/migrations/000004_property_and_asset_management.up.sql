-- Property catalogue: lookups, properties, units, amenities, parking, staff assignments.
CREATE TABLE IF NOT EXISTS property_types (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name        VARCHAR(50)  NOT NULL,
  description VARCHAR(255) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_property_types_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS unit_types (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name        VARCHAR(50)  NOT NULL,
  description VARCHAR(255) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_unit_types_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS amenities (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name        VARCHAR(100) NOT NULL,
  description VARCHAR(255) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_amenities_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS properties (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  owner_id        BIGINT UNSIGNED NOT NULL,
  type_id         BIGINT UNSIGNED NOT NULL,
  name            VARCHAR(150) NOT NULL,
  code            VARCHAR(30)  NULL,
  address         VARCHAR(255) NOT NULL,
  estate          VARCHAR(100) NULL,
  town            VARCHAR(100) NULL,
  county          VARCHAR(100) NOT NULL,
  postal_code     VARCHAR(20)  NULL,
  country_code    CHAR(2)      NOT NULL DEFAULT 'KE',
  latitude        DECIMAL(10,7) NULL,
  longitude       DECIMAL(10,7) NULL,
  year_built      SMALLINT UNSIGNED NULL,
  status          VARCHAR(30)  NOT NULL DEFAULT 'active',
  notes           TEXT NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at      TIMESTAMP NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_properties_org_code (organization_id, code),
  KEY idx_properties_org (organization_id),
  KEY idx_properties_owner (owner_id),
  KEY idx_properties_type (type_id),
  KEY idx_properties_county (county),
  CONSTRAINT fk_properties_org   FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_properties_owner FOREIGN KEY (owner_id) REFERENCES owners (id) ON DELETE RESTRICT,
  CONSTRAINT fk_properties_type  FOREIGN KEY (type_id) REFERENCES property_types (id) ON DELETE RESTRICT,
  CONSTRAINT chk_properties_status CHECK (status IN ('active','inactive','under_construction','sold'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS units (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  property_id BIGINT UNSIGNED NOT NULL,
  type_id     BIGINT UNSIGNED NOT NULL,
  number      VARCHAR(20) NOT NULL,
  floor       SMALLINT NULL,
  bedrooms    TINYINT UNSIGNED NOT NULL DEFAULT 0,
  bathrooms   DECIMAL(3,1) NOT NULL DEFAULT 1.0,
  square_feet INT UNSIGNED NULL,
  rent        DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  status      VARCHAR(30) NOT NULL DEFAULT 'vacant',
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at  TIMESTAMP NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_units_property_number (property_id, number),
  KEY idx_units_type (type_id),
  KEY idx_units_status (status),
  CONSTRAINT fk_units_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE RESTRICT,
  CONSTRAINT fk_units_type     FOREIGN KEY (type_id) REFERENCES unit_types (id) ON DELETE RESTRICT,
  CONSTRAINT chk_units_status  CHECK (status IN ('vacant','occupied','reserved','maintenance','inactive')),
  CONSTRAINT chk_units_rent    CHECK (rent >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS property_amenities (
  property_id BIGINT UNSIGNED NOT NULL,
  amenity_id  BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (property_id, amenity_id),
  KEY idx_property_amenities_amenity (amenity_id),
  CONSTRAINT fk_pa_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE CASCADE,
  CONSTRAINT fk_pa_amenity  FOREIGN KEY (amenity_id) REFERENCES amenities (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS unit_amenities (
  unit_id    BIGINT UNSIGNED NOT NULL,
  amenity_id BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (unit_id, amenity_id),
  KEY idx_unit_amenities_amenity (amenity_id),
  CONSTRAINT fk_ua_unit    FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE CASCADE,
  CONSTRAINT fk_ua_amenity FOREIGN KEY (amenity_id) REFERENCES amenities (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS parking_spots (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  property_id BIGINT UNSIGNED NOT NULL,
  unit_id     BIGINT UNSIGNED NULL,
  number      VARCHAR(20) NOT NULL,
  fee         DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  status      VARCHAR(30) NOT NULL DEFAULT 'available',
  PRIMARY KEY (id),
  UNIQUE KEY uq_parking_property_number (property_id, number),
  KEY idx_parking_unit (unit_id),
  CONSTRAINT fk_parking_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE CASCADE,
  CONSTRAINT fk_parking_unit     FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS property_agents (
  property_id BIGINT UNSIGNED NOT NULL,
  agent_id    BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (property_id, agent_id),
  KEY idx_property_agents_agent (agent_id),
  CONSTRAINT fk_pag_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE CASCADE,
  CONSTRAINT fk_pag_agent    FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS property_assignments (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  property_id     BIGINT UNSIGNED NOT NULL,
  user_id         BIGINT UNSIGNED NOT NULL,
  assignment_role VARCHAR(30) NOT NULL DEFAULT 'property_manager',
  assigned_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  unassigned_at   TIMESTAMP NULL,
  PRIMARY KEY (id),
  KEY idx_assignments_property (property_id),
  KEY idx_assignments_user (user_id),
  CONSTRAINT fk_assign_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE CASCADE,
  CONSTRAINT fk_assign_user     FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT chk_assign_role    CHECK (assignment_role IN ('property_manager','caretaker'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
