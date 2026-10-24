-- Organization-scoped identity and referential integrity.
--
-- 1. Users are unique per organization (not globally), and a soft-deleted
--    user no longer reserves their email/phone forever.
-- 2. Units carry organization_id, so a lease cannot point at another org's
--    unit. Child rows that had no organization_id get one too.
-- 3. Composite (organization_id, id) foreign keys make cross-org references
--    impossible at the database boundary.
-- 4. At most one active lease per unit, and billing/due days are range-checked.
--
-- Before applying: this fails if two leases are already active on one unit,
-- if a message's sender and receiver both have organization_id NULL (there is
-- no organization to assign it to), or if a lease has billing_day/due_day
-- outside 1-28. Resolve those rows first.

-- ---------------------------------------------------------------------------
-- Users: unique inside an organization; deleted rows release the address.
-- ---------------------------------------------------------------------------
ALTER TABLE users
  DROP INDEX uq_users_email,
  DROP INDEX uq_users_phone,
  ADD COLUMN org_scope    BIGINT UNSIGNED GENERATED ALWAYS AS (IFNULL(organization_id, 0)) STORED,
  ADD COLUMN email_active VARCHAR(255) GENERATED ALWAYS AS (IF(deleted_at IS NULL, email, NULL)) STORED,
  ADD COLUMN phone_active VARCHAR(20)  GENERATED ALWAYS AS (IF(deleted_at IS NULL, phone, NULL)) STORED;

ALTER TABLE users
  ADD UNIQUE KEY uq_users_org_email (org_scope, email_active),
  ADD UNIQUE KEY uq_users_org_phone (org_scope, phone_active);

-- ---------------------------------------------------------------------------
-- Parent keys used by the composite foreign keys below.
-- ---------------------------------------------------------------------------
ALTER TABLE owners     ADD UNIQUE KEY uq_owners_org_id (organization_id, id);
ALTER TABLE tenants    ADD UNIQUE KEY uq_tenants_org_id (organization_id, id);
ALTER TABLE properties ADD UNIQUE KEY uq_properties_org_id (organization_id, id);
ALTER TABLE leases     ADD UNIQUE KEY uq_leases_org_id (organization_id, id);
ALTER TABLE invoices   ADD UNIQUE KEY uq_invoices_org_id (organization_id, id);
ALTER TABLE payments   ADD UNIQUE KEY uq_payments_org_id (organization_id, id);
ALTER TABLE deposits   ADD UNIQUE KEY uq_deposits_org_id (organization_id, id);

-- Properties and their owner must belong to the same organization.
ALTER TABLE properties ADD KEY idx_properties_org_owner (organization_id, owner_id);
ALTER TABLE properties DROP FOREIGN KEY fk_properties_owner;
ALTER TABLE properties
  ADD CONSTRAINT fk_properties_owner_org
  FOREIGN KEY (organization_id, owner_id) REFERENCES owners (organization_id, id)
  ON DELETE RESTRICT;

-- Units now carry the organization, inherited from the property.
ALTER TABLE units ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE units u JOIN properties p ON p.id = u.property_id
SET u.organization_id = p.organization_id;
ALTER TABLE units MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE units ADD UNIQUE KEY uq_units_org_id (organization_id, id);
ALTER TABLE units ADD KEY idx_units_org_property (organization_id, property_id);
ALTER TABLE units DROP FOREIGN KEY fk_units_property;
ALTER TABLE units
  ADD CONSTRAINT fk_units_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_units_property_org
    FOREIGN KEY (organization_id, property_id) REFERENCES properties (organization_id, id)
    ON DELETE RESTRICT;

-- Lease -> unit is now organization-scoped.
ALTER TABLE leases ADD KEY idx_leases_org_unit (organization_id, unit_id);
ALTER TABLE leases DROP FOREIGN KEY fk_leases_unit;
ALTER TABLE leases
  ADD CONSTRAINT fk_leases_unit_org
  FOREIGN KEY (organization_id, unit_id) REFERENCES units (organization_id, id)
  ON DELETE RESTRICT;

-- One active lease per unit. Leaving 'active' releases the unit for the next lease.
ALTER TABLE leases
  ADD COLUMN active_unit_id BIGINT UNSIGNED
    GENERATED ALWAYS AS (IF(status = 'active', unit_id, NULL)) STORED,
  ADD UNIQUE KEY uq_leases_active_unit (active_unit_id);

ALTER TABLE leases
  ADD CONSTRAINT chk_leases_billing_day CHECK (billing_day BETWEEN 1 AND 28),
  ADD CONSTRAINT chk_leases_due_day    CHECK (due_day BETWEEN 1 AND 28);

-- ---------------------------------------------------------------------------
-- Lease children: organization_id + composite lease FKs.
-- ---------------------------------------------------------------------------
ALTER TABLE lease_tenants ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE lease_tenants lt JOIN leases l ON l.id = lt.lease_id
SET lt.organization_id = l.organization_id;
ALTER TABLE lease_tenants MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE lease_tenants
  ADD KEY idx_lt_org_lease (organization_id, lease_id),
  ADD KEY idx_lt_org_tenant (organization_id, tenant_id),
  DROP FOREIGN KEY fk_lt_lease,
  DROP FOREIGN KEY fk_lt_tenant,
  ADD CONSTRAINT fk_lt_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_lt_lease_org
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE CASCADE,
  ADD CONSTRAINT fk_lt_tenant_org
    FOREIGN KEY (organization_id, tenant_id) REFERENCES tenants (organization_id, id)
    ON DELETE RESTRICT;

ALTER TABLE deposits ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE deposits d JOIN leases l ON l.id = d.lease_id
SET d.organization_id = l.organization_id;
ALTER TABLE deposits MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE deposits
  ADD KEY idx_deposits_org_lease (organization_id, lease_id),
  DROP FOREIGN KEY fk_deposits_lease,
  ADD CONSTRAINT fk_deposits_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_deposits_lease_org
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT;

ALTER TABLE deposit_refunds ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE deposit_refunds r JOIN deposits d ON d.id = r.deposit_id
SET r.organization_id = d.organization_id;
ALTER TABLE deposit_refunds MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE deposit_refunds
  ADD KEY idx_refunds_org_deposit (organization_id, deposit_id),
  DROP FOREIGN KEY fk_refunds_deposit,
  ADD CONSTRAINT fk_refunds_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_refunds_deposit_org
    FOREIGN KEY (organization_id, deposit_id) REFERENCES deposits (organization_id, id)
    ON DELETE RESTRICT;

ALTER TABLE documents ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE documents d JOIN leases l ON l.id = d.lease_id
SET d.organization_id = l.organization_id;
ALTER TABLE documents MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE documents
  ADD KEY idx_documents_org_lease (organization_id, lease_id),
  DROP FOREIGN KEY fk_documents_lease,
  ADD CONSTRAINT fk_documents_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_documents_lease_org
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE CASCADE;

ALTER TABLE insurance_policies ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE insurance_policies i JOIN leases l ON l.id = i.lease_id
SET i.organization_id = l.organization_id;
ALTER TABLE insurance_policies MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE insurance_policies
  ADD KEY idx_insurance_org_lease (organization_id, lease_id),
  DROP FOREIGN KEY fk_insurance_lease,
  ADD CONSTRAINT fk_insurance_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_insurance_lease_org
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- Billing children: organization_id + composite FKs. This also gives the
-- ledger and allocation triggers a trustworthy organization boundary.
-- ---------------------------------------------------------------------------
ALTER TABLE invoices ADD KEY idx_invoices_org_lease (organization_id, lease_id);
ALTER TABLE invoices DROP FOREIGN KEY fk_invoices_lease;
ALTER TABLE invoices
  ADD CONSTRAINT fk_invoices_lease_org
  FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
  ON DELETE RESTRICT;

ALTER TABLE invoice_items ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE invoice_items ii JOIN invoices i ON i.id = ii.invoice_id
SET ii.organization_id = i.organization_id;
ALTER TABLE invoice_items MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE invoice_items
  ADD KEY idx_items_org_invoice (organization_id, invoice_id),
  DROP FOREIGN KEY fk_invoice_items_invoice,
  ADD CONSTRAINT fk_items_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_items_invoice_org
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE CASCADE;

ALTER TABLE payments ADD KEY idx_payments_org_lease (organization_id, lease_id);
ALTER TABLE payments DROP FOREIGN KEY fk_payments_lease;
ALTER TABLE payments
  ADD CONSTRAINT fk_payments_lease_org
  FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
  ON DELETE RESTRICT;

ALTER TABLE payment_allocations ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE payment_allocations pa JOIN payments p ON p.id = pa.payment_id
SET pa.organization_id = p.organization_id;
ALTER TABLE payment_allocations MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE payment_allocations
  ADD KEY idx_alloc_org_payment (organization_id, payment_id),
  ADD KEY idx_alloc_org_invoice (organization_id, invoice_id),
  DROP FOREIGN KEY fk_alloc_payment,
  DROP FOREIGN KEY fk_alloc_invoice,
  ADD CONSTRAINT fk_alloc_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_alloc_payment_org
    FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
    ON DELETE CASCADE,
  ADD CONSTRAINT fk_alloc_invoice_org
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT;

ALTER TABLE late_fees ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE late_fees lf JOIN invoices i ON i.id = lf.invoice_id
SET lf.organization_id = i.organization_id;
ALTER TABLE late_fees MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE late_fees
  ADD KEY idx_late_fees_org_invoice (organization_id, invoice_id),
  DROP FOREIGN KEY fk_late_fees_invoice,
  ADD CONSTRAINT fk_late_fees_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_late_fees_invoice_org
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT;

-- M-Pesa links are optional, so ON DELETE SET NULL cannot be used on a
-- composite key (it would try to null organization_id). RESTRICT is correct.
ALTER TABLE mpesa_transactions ADD KEY idx_mpesa_org_payment (organization_id, payment_id);
ALTER TABLE mpesa_transactions DROP FOREIGN KEY fk_mpesa_payment;
ALTER TABLE mpesa_transactions
  ADD CONSTRAINT fk_mpesa_payment_org
  FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
  ON DELETE RESTRICT;

ALTER TABLE mpesa_stk_requests
  ADD KEY idx_stk_org_lease (organization_id, lease_id),
  ADD KEY idx_stk_org_invoice (organization_id, invoice_id),
  ADD KEY idx_stk_org_payment (organization_id, payment_id),
  DROP FOREIGN KEY fk_stk_lease,
  DROP FOREIGN KEY fk_stk_invoice,
  DROP FOREIGN KEY fk_stk_payment,
  ADD CONSTRAINT fk_stk_lease_org
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT fk_stk_invoice_org
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT fk_stk_payment_org
    FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
    ON DELETE RESTRICT;

-- ---------------------------------------------------------------------------
-- Messages and reviews belong to an organization.
-- ---------------------------------------------------------------------------
ALTER TABLE messages ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE messages m
JOIN users s ON s.id = m.sender_id
JOIN users r ON r.id = m.receiver_id
SET m.organization_id = COALESCE(s.organization_id, r.organization_id)
WHERE m.organization_id IS NULL;
ALTER TABLE messages MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE messages
  ADD KEY idx_messages_org_date (organization_id, date_sent),
  ADD CONSTRAINT fk_messages_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT;

-- Messages must stay inside one organization; global system admins may take part.
CREATE TRIGGER trg_messages_org_ins BEFORE INSERT ON messages
FOR EACH ROW
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.sender_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message sender does not belong to the organization';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.receiver_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message receiver does not belong to the organization';
  END IF;
END;

CREATE TRIGGER trg_messages_org_upd BEFORE UPDATE ON messages
FOR EACH ROW
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.sender_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message sender does not belong to the organization';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.receiver_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message receiver does not belong to the organization';
  END IF;
END;

ALTER TABLE reviews ADD COLUMN organization_id BIGINT UNSIGNED NULL AFTER id;
UPDATE reviews r JOIN properties p ON p.id = r.property_id
SET r.organization_id = p.organization_id;
ALTER TABLE reviews MODIFY organization_id BIGINT UNSIGNED NOT NULL;
ALTER TABLE reviews
  ADD KEY idx_reviews_org_property (organization_id, property_id),
  ADD KEY idx_reviews_org_tenant (organization_id, tenant_id),
  DROP FOREIGN KEY fk_reviews_tenant,
  DROP FOREIGN KEY fk_reviews_property,
  ADD CONSTRAINT fk_reviews_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  ADD CONSTRAINT fk_reviews_tenant_org
    FOREIGN KEY (organization_id, tenant_id) REFERENCES tenants (organization_id, id)
    ON DELETE CASCADE,
  ADD CONSTRAINT fk_reviews_property_org
    FOREIGN KEY (organization_id, property_id) REFERENCES properties (organization_id, id)
    ON DELETE CASCADE;
