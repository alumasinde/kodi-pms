-- Remove seeded grants and permissions first so no FK blocks the rollback.
DELETE rp FROM role_permissions rp
JOIN permissions p ON p.id = rp.permission_id
WHERE p.code IN (
  'organizations.view', 'organizations.manage',
  'properties.view', 'properties.create', 'properties.update', 'properties.delete',
  'units.view', 'units.create', 'units.update', 'units.delete',
  'tenants.view', 'tenants.view_sensitive', 'tenants.create', 'tenants.update', 'tenants.delete',
  'leases.view', 'leases.create', 'leases.update', 'leases.delete', 'leases.activate', 'leases.terminate', 'leases.renew',
  'invoices.view', 'invoices.create', 'invoices.update', 'invoices.delete',
  'payments.view', 'payments.create', 'payments.reconcile', 'payments.reverse',
  'utilities.view', 'utilities.create', 'utilities.update', 'utilities.record_reading',
  'maintenance.view', 'maintenance.create', 'maintenance.update', 'maintenance.delete', 'maintenance.assign', 'maintenance.close',
  'users.view', 'users.create', 'users.update', 'users.delete',
  'roles.view', 'roles.create', 'roles.update', 'roles.delete',
  'settings.view', 'settings.update',
  'notifications.view', 'notifications.send',
  'dashboard.view', 'reports.view'
);

DELETE FROM permissions
WHERE code IN (
  'organizations.view', 'organizations.manage',
  'properties.view', 'properties.create', 'properties.update', 'properties.delete',
  'units.view', 'units.create', 'units.update', 'units.delete',
  'tenants.view', 'tenants.view_sensitive', 'tenants.create', 'tenants.update', 'tenants.delete',
  'leases.view', 'leases.create', 'leases.update', 'leases.delete', 'leases.activate', 'leases.terminate', 'leases.renew',
  'invoices.view', 'invoices.create', 'invoices.update', 'invoices.delete',
  'payments.view', 'payments.create', 'payments.reconcile', 'payments.reverse',
  'utilities.view', 'utilities.create', 'utilities.update', 'utilities.record_reading',
  'maintenance.view', 'maintenance.create', 'maintenance.update', 'maintenance.delete', 'maintenance.assign', 'maintenance.close',
  'users.view', 'users.create', 'users.update', 'users.delete',
  'roles.view', 'roles.create', 'roles.update', 'roles.delete',
  'settings.view', 'settings.update',
  'notifications.view', 'notifications.send',
  'dashboard.view', 'reports.view'
);

-- Remove seeded built-in roles only if nobody holds them (keeps rollback from breaking user_roles).
DELETE r FROM roles r
WHERE r.name IN ('system_admin','org_admin','landlord','property_manager','caretaker','agent','accountant','tenant','contractor')
  AND r.organization_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM user_roles ur WHERE ur.role_id = r.id);

DELETE FROM system_settings
WHERE organization_id IS NULL
  AND setting_key IN ('currency_code','currency_symbol','rent_due_day','late_fee_grace_days','late_fee_type',
                      'late_fee_value','default_deposit_months','invoice_prefix','vat_rate');
