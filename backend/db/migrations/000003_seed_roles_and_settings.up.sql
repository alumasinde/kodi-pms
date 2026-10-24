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

-- Permission catalogue. Codes are module.action; the API enforces them and the UI hides by them.
INSERT IGNORE INTO permissions (code, description) VALUES
  ('organizations.view',             'View organizations and their domains'),
  ('organizations.manage',           'Create and manage organizations and domains'),
  ('properties.view',                'View properties'),
  ('properties.create',              'Create properties'),
  ('properties.update',              'Update properties'),
  ('properties.delete',              'Delete properties'),
  ('units.view',                     'View units'),
  ('units.create',                   'Create units'),
  ('units.update',                   'Update units'),
  ('units.delete',                   'Delete units'),
  ('tenants.view',                   'View tenant profiles'),
  ('tenants.view_sensitive',         'View tenant national ID, KRA PIN, income and employer'),
  ('tenants.create',                 'Create tenants'),
  ('tenants.update',                 'Update tenants'),
  ('tenants.delete',                 'Delete tenants'),
  ('leases.view',                    'View leases'),
  ('leases.create',                  'Create draft leases'),
  ('leases.update',                  'Update leases'),
  ('leases.delete',                  'Delete draft leases'),
  ('leases.activate',                'Activate leases'),
  ('leases.terminate',               'Terminate leases'),
  ('leases.renew',                   'Renew leases'),
  ('invoices.view',                  'View invoices'),
  ('invoices.create',                'Create invoices'),
  ('invoices.update',                'Update invoices'),
  ('invoices.delete',                'Delete draft invoices'),
  ('payments.view',                  'View payments'),
  ('payments.create',                'Record payments'),
  ('payments.reconcile',             'Reconcile unmatched M-Pesa and bank payments'),
  ('payments.reverse',               'Reverse payments'),
  ('utilities.view',                 'View meters, readings and utility bills'),
  ('utilities.create',               'Create meters and utility bills'),
  ('utilities.update',               'Update meters and utility bills'),
  ('utilities.record_reading',       'Record meter readings'),
  ('maintenance.view',               'View maintenance requests and jobs'),
  ('maintenance.create',             'Create maintenance requests'),
  ('maintenance.update',             'Update maintenance requests and jobs'),
  ('maintenance.delete',             'Delete maintenance records'),
  ('maintenance.assign',             'Assign maintenance work'),
  ('maintenance.close',              'Close maintenance requests'),
  ('users.view',                     'View users'),
  ('users.create',                   'Invite and create users'),
  ('users.update',                   'Update users and their roles'),
  ('users.delete',                   'Deactivate users'),
  ('roles.view',                     'View roles and permissions'),
  ('roles.create',                   'Create custom roles'),
  ('roles.update',                   'Update custom roles'),
  ('roles.delete',                   'Delete custom roles'),
  ('settings.view',                  'View organization settings'),
  ('settings.update',                'Update organization settings'),
  ('notifications.view',             'View notifications and messages'),
  ('notifications.send',             'Send notifications and messages'),
  ('dashboard.view',                 'View the dashboard'),
  ('reports.view',                   'View reports');

-- Default access matrix (README section 4.3).
-- Platform admins get everything except tenant PII; sensitive tenant fields are an org-level grant.
INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'system_admin' AND r.organization_id IS NULL
  AND p.code <> 'tenants.view_sensitive';

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'org_admin' AND r.organization_id IS NULL
  AND p.code NOT LIKE 'organizations.%';

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'landlord' AND r.organization_id IS NULL
  AND p.code IN (
    'properties.view', 'units.view', 'tenants.view', 'leases.view',
    'invoices.view', 'payments.view', 'utilities.view', 'maintenance.view',
    'notifications.view', 'dashboard.view', 'reports.view'
  );

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'property_manager' AND r.organization_id IS NULL
  AND p.code IN (
    'properties.view', 'properties.update',
    'units.view', 'units.create', 'units.update',
    'tenants.view', 'tenants.view_sensitive', 'tenants.create', 'tenants.update',
    'leases.view', 'leases.create', 'leases.update', 'leases.activate', 'leases.terminate', 'leases.renew',
    'invoices.view', 'invoices.create', 'invoices.update',
    'payments.view', 'payments.create',
    'utilities.view', 'utilities.create', 'utilities.update',
    'maintenance.view', 'maintenance.create', 'maintenance.update', 'maintenance.assign', 'maintenance.close',
    'settings.view', 'notifications.view', 'notifications.send',
    'dashboard.view', 'reports.view'
  );

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'caretaker' AND r.organization_id IS NULL
  AND p.code IN (
    'properties.view', 'units.view', 'tenants.view', 'leases.view',
    'utilities.view', 'utilities.record_reading',
    'maintenance.view', 'maintenance.update',
    'dashboard.view', 'reports.view'
  );

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'agent' AND r.organization_id IS NULL
  AND p.code IN (
    'properties.view', 'units.view', 'tenants.view', 'tenants.create', 'tenants.update',
    'leases.view', 'leases.create',
    'dashboard.view', 'reports.view'
  );

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'accountant' AND r.organization_id IS NULL
  AND p.code IN (
    'properties.view', 'units.view', 'tenants.view', 'leases.view',
    'invoices.view', 'invoices.create', 'invoices.update', 'invoices.delete',
    'payments.view', 'payments.create', 'payments.reconcile', 'payments.reverse',
    'utilities.view', 'dashboard.view', 'reports.view'
  );

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'tenant' AND r.organization_id IS NULL
  AND p.code IN (
    'tenants.view', 'leases.view', 'invoices.view', 'payments.view',
    'maintenance.view', 'maintenance.create',
    'notifications.view', 'dashboard.view', 'reports.view'
  );

INSERT IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'contractor' AND r.organization_id IS NULL
  AND p.code IN ('maintenance.view', 'maintenance.update');
