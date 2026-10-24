-- Kodi PMS demo dataset.
--
-- Apply only to a dedicated, disposable demo database, after the migrations:
--   (echo "SET @demo_seed_confirmed = 'yes';"; cat db/demo/demo_seed.sql) \
--     | mysql -u <user> -p <database>
--
-- The guard below refuses to run unless @demo_seed_confirmed is set in the same
-- session. This seed creates accounts with published credentials, so never
-- apply it to a production database.
--
-- Demo login:  demo@kodipms.co.ke
-- Password:    Demo@2026!
-- Quick PIN:   2026            (demo admin only; requires a password login on
--                              the device first, then the refresh cookie)
-- Domains:     localhost  and  127.0.0.1
--
-- The script is idempotent: rows use ids in the 9000 range and are inserted
-- with INSERT IGNORE (allocations are guarded with NOT EXISTS because the
-- allocation triggers run before duplicate-key checks). Re-running it will not
-- duplicate rows and will not overwrite demo data edited during a session.
-- Apply it to a clean demo database when you want a fresh starting point.

SET NAMES utf8mb4;

CREATE TEMPORARY TABLE demo_seed_guard (
  confirmed TINYINT NOT NULL,
  CHECK (confirmed = 1)
);
INSERT INTO demo_seed_guard (confirmed)
VALUES (IF(COALESCE(@demo_seed_confirmed, 'no') = 'yes', 1, 0));
DROP TEMPORARY TABLE demo_seed_guard;

SET @today        = CURDATE();
SET @period_start = CAST(DATE_FORMAT(@today, '%Y-%m-01') AS DATE);
SET @period_end   = LAST_DAY(@today);
SET @due_date     = DATE_ADD(@period_start, INTERVAL 4 DAY);

SET @lease1_start = DATE_SUB(@period_start, INTERVAL 10 MONTH);
SET @lease1_end   = DATE_SUB(DATE_ADD(@lease1_start, INTERVAL 1 YEAR), INTERVAL 1 DAY);
SET @lease2_start = DATE_SUB(@period_start, INTERVAL 4 MONTH);
SET @lease2_end   = DATE_SUB(DATE_ADD(@lease2_start, INTERVAL 1 YEAR), INTERVAL 1 DAY);

SET @payment1_at  = DATE_ADD(DATE_ADD(@period_start, INTERVAL 1 DAY), INTERVAL 9 HOUR);
SET @payment2_at  = DATE_ADD(DATE_ADD(@period_start, INTERVAL 3 DAY), INTERVAL 14 HOUR);
SET @unmatched_at = DATE_ADD(DATE_ADD(@period_start, INTERVAL 6 DAY), INTERVAL 11 HOUR);

SET @mpesa_paybill = (SELECT id FROM payment_methods WHERE code = 'mpesa_paybill' LIMIT 1);
SET @pt_apartment  = (SELECT id FROM property_types WHERE name = 'Apartment Block' LIMIT 1);
SET @ut_2bed       = (SELECT id FROM unit_types WHERE name = '2 Bedroom' LIMIT 1);
SET @ut_1bed       = (SELECT id FROM unit_types WHERE name = '1 Bedroom' LIMIT 1);
SET @ut_bedsitter  = (SELECT id FROM unit_types WHERE name = 'Bedsitter' LIMIT 1);

-- Password hashes below are bcrypt cost 12 for "Demo@2026!".
-- The demo admin's pin_hash is bcrypt cost 12 for PIN "2026".
START TRANSACTION;

INSERT IGNORE INTO organizations
  (id, org_name, slug, legal_name, kra_pin, email, phone, org_address, county, country_code, currency_code, timezone, org_status)
VALUES
  (9001, 'Kodi Demo Properties', 'kodi-demo', 'Kodi Demo Properties Ltd', 'P051234567X',
   'hello@kodipms.co.ke', '+254700000000', 'Wood Avenue, Kilimani', 'Nairobi', 'KE', 'KES', 'Africa/Nairobi', 'active');

INSERT IGNORE INTO organization_domains (id, organization_id, domain, is_primary, verified_at)
VALUES
  (9001, 9001, 'localhost', 1, NOW()),
  (9002, 9001, '127.0.0.1', 0, NOW());

INSERT IGNORE INTO users
  (id, organization_id, email, phone, password_hash, pin_hash, must_change_password, first_name, last_name, status, email_verified_at, phone_verified_at)
VALUES
  (9001, 9001, 'demo@kodipms.co.ke', '+254700000001',
   '$2y$12$lvDZYxg40zWSK8ebMCO6yeLbsOAiqyRZWzBXzGZ/noXmfKElRESDO',
   '$2y$12$Lj/9jGwMtyfsnvHbeA/28uUWZiziASMAdxT0ldioneqkyqzqtLvUy', 0,
   'Demo', 'Admin', 'active', NOW(), NOW()),
  (9002, 9001, 'owner.demo@kodipms.co.ke', '+254700000002',
   '$2y$12$lvDZYxg40zWSK8ebMCO6yeLbsOAiqyRZWzBXzGZ/noXmfKElRESDO', NULL, 0,
   'Peter', 'Mwangi', 'inactive', NOW(), NOW()),
  (9003, 9001, 'brian.otieno@example.test', '+254700000003',
   '$2y$12$lvDZYxg40zWSK8ebMCO6yeLbsOAiqyRZWzBXzGZ/noXmfKElRESDO', NULL, 0,
   'Brian', 'Otieno', 'inactive', NOW(), NOW()),
  (9004, 9001, 'faith.wanjiku@example.test', '+254700000004',
   '$2y$12$lvDZYxg40zWSK8ebMCO6yeLbsOAiqyRZWzBXzGZ/noXmfKElRESDO', NULL, 0,
   'Faith', 'Wanjiku', 'inactive', NOW(), NOW());

INSERT IGNORE INTO user_roles (user_id, role_id)
SELECT 9001, id FROM roles WHERE name = 'org_admin' AND organization_id IS NULL;

INSERT IGNORE INTO user_roles (user_id, role_id)
SELECT 9002, id FROM roles WHERE name = 'landlord' AND organization_id IS NULL;

INSERT IGNORE INTO user_roles (user_id, role_id)
SELECT 9003, id FROM roles WHERE name = 'tenant' AND organization_id IS NULL;

INSERT IGNORE INTO user_roles (user_id, role_id)
SELECT 9004, id FROM roles WHERE name = 'tenant' AND organization_id IS NULL;

INSERT IGNORE INTO owners
  (id, organization_id, user_id, company, management_fee_percent, payout_method)
VALUES
  (9001, 9001, 9002, 'Mwangi Holdings', 8.00, 'mpesa');

INSERT IGNORE INTO tenants
  (id, organization_id, user_id, emergency_contact, emergency_phone, employment, employer_name)
VALUES
  (9001, 9001, 9003, 'Mary Otieno', '+254711000003', 'Employed', 'Safaricom PLC'),
  (9002, 9001, 9004, 'James Wanjiku', '+254711000004', 'Self-employed', 'Wanjiku Studios');

INSERT IGNORE INTO properties
  (id, organization_id, owner_id, type_id, name, code, address, estate, town, county, postal_code, country_code, status, notes)
VALUES
  (9001, 9001, 9001, @pt_apartment, 'Kilimani Heights', 'KD-001', 'Wood Avenue, Kilimani',
   'Kilimani', 'Nairobi', 'Nairobi', '00100', 'KE', 'active', 'Four-unit demo block.');

INSERT IGNORE INTO units
  (id, organization_id, property_id, type_id, number, floor, bedrooms, bathrooms, square_feet, rent, status)
VALUES
  (9001, 9001, 9001, @ut_2bed,      'A1', 1, 2, 2.0, 95, 45000.00, 'occupied'),
  (9002, 9001, 9001, @ut_1bed,      'A2', 1, 1, 1.0, 65, 35000.00, 'occupied'),
  (9003, 9001, 9001, @ut_bedsitter, 'A3', 2, 0, 1.0, 35, 18000.00, 'vacant'),
  (9004, 9001, 9001, @ut_2bed,      'A4', 2, 2, 2.0, 95, 48000.00, 'maintenance');

INSERT IGNORE INTO property_amenities (property_id, amenity_id)
SELECT 9001, id FROM amenities
WHERE name IN ('Borehole','Backup Generator','CCTV','24hr Security','Parking');

INSERT IGNORE INTO unit_amenities (unit_id, amenity_id)
SELECT u.id, a.id
FROM units u
JOIN amenities a ON a.name IN ('Balcony','Fibre Internet')
WHERE u.id IN (9001, 9002);

INSERT IGNORE INTO parking_spots (id, property_id, unit_id, number, fee, status)
VALUES
  (9001, 9001, 9001, 'P1', 2000.00, 'assigned'),
  (9002, 9001, NULL, 'P2', 1500.00, 'available'),
  (9003, 9001, NULL, 'P3', 1500.00, 'available');

INSERT IGNORE INTO leases
  (id, organization_id, unit_id, lease_number, start_date, end_date, rent_amount, deposit_amount,
   billing_day, due_day, notice_period_days, status, signed_at, created_by)
VALUES
  (9001, 9001, 9001, CONCAT('KD-L-', YEAR(@lease1_start), '-001'), @lease1_start, @lease1_end,
   45000.00, 90000.00, 1, 5, 30, 'active', DATE_ADD(@lease1_start, INTERVAL 9 HOUR), 9001),
  (9002, 9001, 9002, CONCAT('KD-L-', YEAR(@lease2_start), '-002'), @lease2_start, @lease2_end,
   35000.00, 70000.00, 1, 5, 30, 'active', DATE_ADD(@lease2_start, INTERVAL 9 HOUR), 9001);

INSERT IGNORE INTO lease_tenants (organization_id, lease_id, tenant_id, role)
VALUES
  (9001, 9001, 9001, 'primary'),
  (9001, 9002, 9002, 'primary');

INSERT IGNORE INTO deposits (id, organization_id, lease_id, amount, date_paid, status)
VALUES
  (9001, 9001, 9001, 90000.00, @lease1_start, 'held'),
  (9002, 9001, 9002, 70000.00, @lease2_start, 'held');

INSERT IGNORE INTO invoices
  (id, organization_id, lease_id, invoice_number, invoice_type, billing_period_start, billing_period_end,
   issue_date, due_date, subtotal, tax_total, total, amount_paid, status, notes, created_by)
VALUES
  (9001, 9001, 9001, CONCAT('INV-KD-', DATE_FORMAT(@period_start, '%Y%m'), '-001'), 'rent',
   @period_start, @period_end, @period_start, @due_date, 49375.00, 0.00, 49375.00, 0.00,
   'paid', 'Monthly rent, water, garbage and parking.', 9001),
  (9002, 9001, 9002, CONCAT('INV-KD-', DATE_FORMAT(@period_start, '%Y%m'), '-002'), 'rent',
   @period_start, @period_end, @period_start, @due_date, 36340.00, 0.00, 36340.00, 0.00,
   'partially_paid', 'Monthly rent, water and garbage.', 9001);

INSERT IGNORE INTO utility_meters
  (id, property_id, unit_id, utility_type, meter_number, unit_of_measure, rate_per_unit, status)
VALUES
  (9001, 9001, 9001, 'water', 'WM-KD-A1', 'm3', 150.0000, 'active'),
  (9002, 9001, 9002, 'water', 'WM-KD-A2', 'm3', 150.0000, 'active');

INSERT IGNORE INTO utility_readings
  (id, meter_id, reading_date, previous_reading, current_reading, recorded_by, notes)
VALUES
  (9001, 9001, @today, 110.000, 122.500, 9001, 'Monthly water reading for A1.'),
  (9002, 9002, @today, 55.000, 60.600, 9001, 'Monthly water reading for A2.');

INSERT IGNORE INTO utility_bills
  (id, unit_id, meter_id, reading_id, invoice_id, type, amount, date_due, period_start, period_end, status)
VALUES
  (9001, 9001, 9001, 9001, 9001, 'water', 1875.00, @due_date, @period_start, @period_end, 'billed'),
  (9002, 9002, 9002, 9002, 9002, 'water', 840.00, @due_date, @period_start, @period_end, 'billed');

INSERT IGNORE INTO invoice_items
  (id, organization_id, invoice_id, item_type, description, quantity, unit_price, tax_rate, amount, source_type, source_id)
VALUES
  (9001, 9001, 9001, 'rent',    'Monthly rent - A1',          1.000, 45000.00, 0.00, 45000.00, NULL, NULL),
  (9002, 9001, 9001, 'water',   'Water consumption - 12.5 m3', 12.500,   150.00, 0.00,  1875.00, 'utility_bill', 9001),
  (9003, 9001, 9001, 'garbage', 'Garbage collection',          1.000,   500.00, 0.00,   500.00, NULL, NULL),
  (9004, 9001, 9001, 'parking', 'Parking bay P1',              1.000,  2000.00, 0.00,  2000.00, NULL, NULL),
  (9005, 9001, 9002, 'rent',    'Monthly rent - A2',           1.000, 35000.00, 0.00, 35000.00, NULL, NULL),
  (9006, 9001, 9002, 'water',   'Water consumption - 5.6 m3',   5.600,   150.00, 0.00,   840.00, 'utility_bill', 9002),
  (9007, 9001, 9002, 'garbage', 'Garbage collection',           1.000,   500.00, 0.00,   500.00, NULL, NULL);

INSERT IGNORE INTO payments
  (id, organization_id, lease_id, payment_method_id, amount, paid_at, reference, status, received_by, notes)
VALUES
  (9001, 9001, 9001, @mpesa_paybill, 49375.00, @payment1_at, 'SKQ4DEMO01', 'completed', 9001, 'M-Pesa Paybill.'),
  (9002, 9001, 9002, @mpesa_paybill, 20000.00, @payment2_at, 'SKQ4DEMO02', 'completed', 9001, 'Partial M-Pesa payment.');

INSERT IGNORE INTO payment_allocations (id, organization_id, payment_id, invoice_id, amount)
SELECT 9001, 9001, 9001, 9001, 49375.00 FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM payment_allocations WHERE id = 9001);

INSERT IGNORE INTO payment_allocations (id, organization_id, payment_id, invoice_id, amount)
SELECT 9002, 9001, 9002, 9002, 20000.00 FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM payment_allocations WHERE id = 9002);

INSERT IGNORE INTO mpesa_shortcodes
  (id, organization_id, shortcode, account_type, environment, is_active)
VALUES
  (9001, 9001, '600000', 'paybill', 'sandbox', 1);

INSERT IGNORE INTO mpesa_account_refs
  (id, organization_id, shortcode_id, lease_id, account_reference)
VALUES
  (9001, 9001, 9001, 9001, 'KD-001-A1'),
  (9002, 9001, 9001, 9002, 'KD-001-A2');

INSERT IGNORE INTO mpesa_transactions
  (id, organization_id, trans_id, trans_type, business_short_code, shortcode_id, bill_ref_number, lease_id,
   msisdn, first_name, amount, trans_time, status, payment_id, raw_payload)
VALUES
  (9001, 9001, 'SKQ4DEMO01', 'Pay Bill', '600000', 9001, 'KD-001-A1', 9001, '254700000003', 'BRIAN',
   49375.00, @payment1_at, 'matched', 9001,
   '{"TransID":"SKQ4DEMO01","TransAmount":"49375.00","BillRefNumber":"KD-001-A1","MSISDN":"254700000003"}'),
  (9002, 9001, 'SKQ4DEMO02', 'Pay Bill', '600000', 9001, 'KD-001-A2', 9002, '254700000004', 'FAITH',
   20000.00, @payment2_at, 'matched', 9002,
   '{"TransID":"SKQ4DEMO02","TransAmount":"20000.00","BillRefNumber":"KD-001-A2","MSISDN":"254700000004"}'),
  (9003, 9001, 'SKQ4DEMO03', 'Pay Bill', '600000', 9001, 'KD-001-A1', 9001, '254700000005', 'UNKNOWN',
   10000.00, @unmatched_at, 'unmatched', NULL,
   '{"TransID":"SKQ4DEMO03","TransAmount":"10000.00","BillRefNumber":"KD-001-A1","MSISDN":"254700000005"}');

INSERT IGNORE INTO mpesa_stk_requests
  (id, organization_id, shortcode_id, lease_id, invoice_id, phone, amount, merchant_request_id, checkout_request_id,
   status, result_code, result_desc, mpesa_receipt, payment_id)
VALUES
  (9001, 9001, 9001, 9001, 9001, '+254700000003', 49375.00, 'demo-merchant-1', 'ws_CO_DEMO_9001',
   'success', 0, 'The service request is processed successfully.', 'SKQ4DEMO01', 9001);

INSERT IGNORE INTO contractors
  (id, organization_id, user_id, company, contact, phone, email, specialty, status)
VALUES
  (9001, 9001, NULL, 'SwiftFix Plumbers', 'John Kariuki', '+254722000001', 'jobs@swiftfix.test',
   'Plumbing', 'active');

INSERT IGNORE INTO maintenance_requests
  (id, organization_id, unit_id, tenant_id, assigned_to, title, description, priority, status, date_requested)
VALUES
  (9001, 9001, 9001, 9001, 9001, 'Kitchen sink leaking',
   'Water is pooling under the kitchen sink and the cabinet is getting damp.', 'high', 'assigned',
   DATE_SUB(@today, INTERVAL 2 DAY));

INSERT IGNORE INTO maintenance_jobs
  (id, request_id, contractor_id, date_scheduled, status, notes)
VALUES
  (9001, 9001, 9001, DATE_ADD(@today, INTERVAL 1 DAY), 'scheduled',
   'Plumber to inspect the trap and replace the seal.');

INSERT IGNORE INTO maintenance_logs
  (id, request_id, user_id, event_type, from_status, to_status, message)
VALUES
  (9001, 9001, 9001, 'status_change', 'open', 'assigned', 'Assigned to SwiftFix Plumbers.');

INSERT IGNORE INTO inspections
  (id, unit_id, inspector_id, date_conducted, type, status, notes)
VALUES
  (9001, 9003, 9001, DATE_ADD(@today, INTERVAL 3 DAY), 'move_in', 'scheduled',
   'Pre-let inspection for A3.');

INSERT IGNORE INTO inspection_items
  (id, inspection_id, area, condition_rating, is_acceptable, comments)
VALUES
  (9001, 9001, 'Kitchen', 'good', 1, 'Clean and functional.'),
  (9002, 9001, 'Bathroom', 'fair', 1, 'Minor grout wear.');

INSERT IGNORE INTO reviews (id, organization_id, tenant_id, property_id, rating, content, date_submitted)
VALUES
  (9001, 9001, 9001, 9001, 4, 'Quiet compound and responsive management.',
   DATE_SUB(@today, INTERVAL 1 MONTH));

INSERT IGNORE INTO notifications
  (id, organization_id, user_id, channel, type, title, body, payload, status, sent_at, read_at)
VALUES
  (9001, 9001, 9001, 'in_app', 'demo_welcome', 'Welcome to the Kodi PMS demo',
   'This workspace contains a live portfolio, leases, invoices, payments and maintenance data.',
   '{"demo":true}', 'read', NOW(), NOW());

COMMIT;
