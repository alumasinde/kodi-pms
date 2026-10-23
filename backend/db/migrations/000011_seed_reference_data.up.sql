-- Idempotent lookup data suited to the Kenyan market.
INSERT IGNORE INTO property_types (name, description) VALUES
  ('Apartment Block', 'Multi-unit residential block'),
  ('Bungalow', 'Single-storey standalone home'),
  ('Maisonette', 'Multi-storey standalone or semi-detached home'),
  ('Townhouse', 'Terraced / gated community house'),
  ('Commercial', 'Offices and shops'),
  ('Mixed Use', 'Residential and commercial'),
  ('Hostel', 'Student or shared accommodation'),
  ('Servant Quarters (DSQ)', 'Detached staff quarters');

INSERT IGNORE INTO unit_types (name, description) VALUES
  ('Single Room', 'One room, shared facilities'),
  ('Bedsitter', 'Single room with own bathroom and kitchenette'),
  ('Studio', 'Open-plan studio'),
  ('1 Bedroom', NULL),
  ('2 Bedroom', NULL),
  ('3 Bedroom', NULL),
  ('4 Bedroom', NULL),
  ('Maisonette', NULL),
  ('Shop', 'Retail space'),
  ('Office', 'Office space');

INSERT IGNORE INTO amenities (name, description) VALUES
  ('Borehole', NULL), ('Backup Generator', NULL), ('Solar Water Heating', NULL),
  ('CCTV', NULL), ('24hr Security', NULL), ('Electric Fence', NULL),
  ('Parking', NULL), ('Lift', NULL), ('Gym', NULL), ('Swimming Pool', NULL),
  ('Fibre Internet', NULL), ('Balcony', NULL), ('Garbage Collection', NULL),
  ('Playground', NULL), ('Water Tank', NULL);

INSERT IGNORE INTO payment_methods (code, name, description) VALUES
  ('mpesa_paybill', 'M-Pesa Paybill', 'C2B via Paybill'),
  ('mpesa_till',    'M-Pesa Till',    'C2B via Buy Goods till'),
  ('mpesa_stk',     'M-Pesa STK Push','Prompt sent to tenant phone'),
  ('bank_transfer', 'Bank Transfer',  'EFT / RTGS / PesaLink'),
  ('cash',          'Cash',           NULL),
  ('cheque',        'Cheque',         NULL),
  ('card',          'Card',           NULL);
