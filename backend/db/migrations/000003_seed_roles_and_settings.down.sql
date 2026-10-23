-- Remove seeded roles only if nobody holds them (keeps rollback from breaking user_roles).
DELETE r FROM roles r
WHERE r.name IN ('system_admin','org_admin','landlord','property_manager','caretaker','agent','accountant','tenant','contractor')
  AND NOT EXISTS (SELECT 1 FROM user_roles ur WHERE ur.role_id = r.id);

DELETE FROM system_settings
WHERE organization_id IS NULL
  AND setting_key IN ('currency_code','currency_symbol','rent_due_day','late_fee_grace_days','late_fee_type',
                      'late_fee_value','default_deposit_months','invoice_prefix','vat_rate');
