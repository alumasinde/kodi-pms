-- Reverse of 000013. Rolling this back can fail if data now contains duplicate
-- emails/phones across organizations or multiple active leases per unit;
-- clean those rows before rolling back.

DROP TRIGGER IF EXISTS trg_messages_org_ins;
DROP TRIGGER IF EXISTS trg_messages_org_upd;

ALTER TABLE reviews
  DROP FOREIGN KEY fk_reviews_org,
  DROP FOREIGN KEY fk_reviews_tenant_org,
  DROP FOREIGN KEY fk_reviews_property_org,
  DROP KEY idx_reviews_org_property,
  DROP KEY idx_reviews_org_tenant,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_reviews_tenant
    FOREIGN KEY (tenant_id) REFERENCES tenants (id) ON DELETE CASCADE,
  ADD CONSTRAINT fk_reviews_property
    FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE CASCADE;

ALTER TABLE messages
  DROP FOREIGN KEY fk_messages_org,
  DROP KEY idx_messages_org_date,
  DROP COLUMN organization_id;

ALTER TABLE mpesa_stk_requests
  DROP FOREIGN KEY fk_stk_lease_org,
  DROP FOREIGN KEY fk_stk_invoice_org,
  DROP FOREIGN KEY fk_stk_payment_org,
  DROP KEY idx_stk_org_lease,
  DROP KEY idx_stk_org_invoice,
  DROP KEY idx_stk_org_payment,
  ADD CONSTRAINT fk_stk_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE SET NULL,
  ADD CONSTRAINT fk_stk_invoice
    FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE SET NULL,
  ADD CONSTRAINT fk_stk_payment
    FOREIGN KEY (payment_id) REFERENCES payments (id) ON DELETE SET NULL;

ALTER TABLE mpesa_transactions
  DROP FOREIGN KEY fk_mpesa_payment_org,
  DROP KEY idx_mpesa_org_payment,
  ADD CONSTRAINT fk_mpesa_payment
    FOREIGN KEY (payment_id) REFERENCES payments (id) ON DELETE SET NULL;

ALTER TABLE late_fees
  DROP FOREIGN KEY fk_late_fees_org,
  DROP FOREIGN KEY fk_late_fees_invoice_org,
  DROP KEY idx_late_fees_org_invoice,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_late_fees_invoice
    FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE RESTRICT;

ALTER TABLE payment_allocations
  DROP FOREIGN KEY fk_alloc_org,
  DROP FOREIGN KEY fk_alloc_payment_org,
  DROP FOREIGN KEY fk_alloc_invoice_org,
  DROP KEY idx_alloc_org_payment,
  DROP KEY idx_alloc_org_invoice,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_alloc_payment
    FOREIGN KEY (payment_id) REFERENCES payments (id) ON DELETE CASCADE,
  ADD CONSTRAINT fk_alloc_invoice
    FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE RESTRICT;

ALTER TABLE payments
  DROP FOREIGN KEY fk_payments_lease_org,
  DROP KEY idx_payments_org_lease,
  ADD CONSTRAINT fk_payments_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE RESTRICT;

ALTER TABLE invoice_items
  DROP FOREIGN KEY fk_items_org,
  DROP FOREIGN KEY fk_items_invoice_org,
  DROP KEY idx_items_org_invoice,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_invoice_items_invoice
    FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE CASCADE;

ALTER TABLE invoices
  DROP FOREIGN KEY fk_invoices_lease_org,
  DROP KEY idx_invoices_org_lease,
  ADD CONSTRAINT fk_invoices_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE RESTRICT;

ALTER TABLE insurance_policies
  DROP FOREIGN KEY fk_insurance_org,
  DROP FOREIGN KEY fk_insurance_lease_org,
  DROP KEY idx_insurance_org_lease,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_insurance_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE CASCADE;

ALTER TABLE documents
  DROP FOREIGN KEY fk_documents_org,
  DROP FOREIGN KEY fk_documents_lease_org,
  DROP KEY idx_documents_org_lease,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_documents_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE CASCADE;

ALTER TABLE deposit_refunds
  DROP FOREIGN KEY fk_refunds_org,
  DROP FOREIGN KEY fk_refunds_deposit_org,
  DROP KEY idx_refunds_org_deposit,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_refunds_deposit
    FOREIGN KEY (deposit_id) REFERENCES deposits (id) ON DELETE RESTRICT;

ALTER TABLE deposits
  DROP FOREIGN KEY fk_deposits_org,
  DROP FOREIGN KEY fk_deposits_lease_org,
  DROP KEY idx_deposits_org_lease,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_deposits_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE RESTRICT;

ALTER TABLE lease_tenants
  DROP FOREIGN KEY fk_lt_org,
  DROP FOREIGN KEY fk_lt_lease_org,
  DROP FOREIGN KEY fk_lt_tenant_org,
  DROP KEY idx_lt_org_lease,
  DROP KEY idx_lt_org_tenant,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_lt_lease
    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE CASCADE,
  ADD CONSTRAINT fk_lt_tenant
    FOREIGN KEY (tenant_id) REFERENCES tenants (id) ON DELETE RESTRICT;

ALTER TABLE leases
  DROP CHECK chk_leases_billing_day,
  DROP CHECK chk_leases_due_day,
  DROP INDEX uq_leases_active_unit,
  DROP COLUMN active_unit_id,
  DROP FOREIGN KEY fk_leases_unit_org,
  DROP KEY idx_leases_org_unit,
  ADD CONSTRAINT fk_leases_unit
    FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE RESTRICT;

ALTER TABLE units
  DROP FOREIGN KEY fk_units_org,
  DROP FOREIGN KEY fk_units_property_org,
  DROP KEY idx_units_org_property,
  DROP INDEX uq_units_org_id,
  DROP COLUMN organization_id,
  ADD CONSTRAINT fk_units_property
    FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE RESTRICT;

ALTER TABLE properties
  DROP FOREIGN KEY fk_properties_owner_org,
  DROP KEY idx_properties_org_owner,
  ADD CONSTRAINT fk_properties_owner
    FOREIGN KEY (owner_id) REFERENCES owners (id) ON DELETE RESTRICT;

ALTER TABLE leases     DROP INDEX uq_leases_org_id;
ALTER TABLE invoices   DROP INDEX uq_invoices_org_id;
ALTER TABLE payments   DROP INDEX uq_payments_org_id;
ALTER TABLE deposits   DROP INDEX uq_deposits_org_id;
ALTER TABLE tenants    DROP INDEX uq_tenants_org_id;
ALTER TABLE owners     DROP INDEX uq_owners_org_id;
ALTER TABLE properties DROP INDEX uq_properties_org_id;

ALTER TABLE users
  DROP INDEX uq_users_org_email,
  DROP INDEX uq_users_org_phone,
  DROP COLUMN email_active,
  DROP COLUMN phone_active,
  DROP COLUMN org_scope,
  ADD UNIQUE KEY uq_users_email (email),
  ADD UNIQUE KEY uq_users_phone (phone);
