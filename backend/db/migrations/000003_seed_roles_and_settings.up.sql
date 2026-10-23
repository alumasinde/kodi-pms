-- Idempotent seed data (INSERT IGNORE relies on the unique keys).
INSERT IGNORE INTO roles (name, description) VALUES
  ('system_admin',     'Platform administrator across all organizations'),
  ('org_admin',        'Administrator of one organization'),
  ('landlord',         'Property owner'),
  ('property_manager', 'Manages assigned properties'),
  ('caretaker',        'On-site caretaker for assigned properties'),
  ('agent',            'Letting / sales agent'),
  ('accountant',       'Finance and reconciliation'),
  ('tenant',           'Resident / lessee'),
  ('contractor',       'External maintenance contractor');

INSERT IGNORE INTO system_settings (organization_id, setting_key, setting_value, value_type, description) VALUES
  (NULL, 'currency_code',           'KES',  'string',  'Default currency'),
  (NULL, 'currency_symbol',         'KSh',  'string',  'Default currency symbol'),
  (NULL, 'rent_due_day',            '5',    'int',     'Day of month rent is due'),
  (NULL, 'late_fee_grace_days',     '5',    'int',     'Days after due date before a late fee applies'),
  (NULL, 'late_fee_type',           'percent', 'string', 'percent or fixed'),
  (NULL, 'late_fee_value',          '5',    'decimal', 'Late fee percent of rent, or fixed KES amount'),
  (NULL, 'default_deposit_months',  '2',    'int',     'Default rent deposit in months of rent'),
  (NULL, 'invoice_prefix',          'INV',  'string',  'Invoice number prefix'),
  (NULL, 'vat_rate',                '16',   'decimal', 'VAT % for commercial leases');
