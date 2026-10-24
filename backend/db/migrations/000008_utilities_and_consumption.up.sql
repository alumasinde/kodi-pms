-- Metered utilities (water, KPLC power, etc.) and the bills derived from readings.
CREATE TABLE IF NOT EXISTS utility_meters (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  property_id     BIGINT UNSIGNED NOT NULL,
  unit_id         BIGINT UNSIGNED NULL COMMENT 'NULL = shared / property-level meter',
  utility_type    VARCHAR(30) NOT NULL,
  meter_number    VARCHAR(50) NOT NULL,
  unit_of_measure VARCHAR(10) NOT NULL DEFAULT 'm3',
  rate_per_unit   DECIMAL(12,4) NOT NULL DEFAULT 0.0000,
  status          VARCHAR(30) NOT NULL DEFAULT 'active',
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_meters_property_number (property_id, meter_number),
  KEY idx_meters_unit (unit_id),
  CONSTRAINT fk_meters_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE RESTRICT,
  CONSTRAINT fk_meters_unit     FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE SET NULL,
  CONSTRAINT chk_meters_type    CHECK (utility_type IN ('water','electricity','gas','garbage','internet','other'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS utility_readings (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  meter_id         BIGINT UNSIGNED NOT NULL,
  reading_date     DATE NOT NULL,
  previous_reading DECIMAL(14,3) NOT NULL DEFAULT 0.000,
  current_reading  DECIMAL(14,3) NOT NULL,
  consumption      DECIMAL(14,3) GENERATED ALWAYS AS (current_reading - previous_reading) STORED,
  recorded_by      BIGINT UNSIGNED NULL,
  notes            VARCHAR(255) NULL,
  created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_readings_meter_date (meter_id, reading_date),
  KEY idx_readings_recorder (recorded_by),
  CONSTRAINT fk_readings_meter    FOREIGN KEY (meter_id) REFERENCES utility_meters (id) ON DELETE CASCADE,
  CONSTRAINT fk_readings_recorder FOREIGN KEY (recorded_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_readings_order   CHECK (current_reading >= previous_reading)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS utility_bills (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  unit_id      BIGINT UNSIGNED NOT NULL,
  meter_id     BIGINT UNSIGNED NULL,
  reading_id   BIGINT UNSIGNED NULL,
  invoice_id   BIGINT UNSIGNED NULL COMMENT 'Set once billed to the tenant',
  type         VARCHAR(50) NOT NULL,
  amount       DECIMAL(12,2) NOT NULL,
  date_due     DATE NOT NULL,
  period_start DATE NOT NULL,
  period_end   DATE NOT NULL,
  status       VARCHAR(30) NOT NULL DEFAULT 'pending',
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_ubills_unit (unit_id),
  KEY idx_ubills_meter (meter_id),
  KEY idx_ubills_invoice (invoice_id),
  UNIQUE KEY uq_ubills_reading (reading_id),
  CONSTRAINT fk_ubills_unit    FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE RESTRICT,
  CONSTRAINT fk_ubills_meter   FOREIGN KEY (meter_id) REFERENCES utility_meters (id) ON DELETE SET NULL,
  CONSTRAINT fk_ubills_reading FOREIGN KEY (reading_id) REFERENCES utility_readings (id) ON DELETE SET NULL,
  CONSTRAINT fk_ubills_invoice FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE SET NULL,
  CONSTRAINT chk_ubills_status CHECK (status IN ('pending','billed','paid','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
