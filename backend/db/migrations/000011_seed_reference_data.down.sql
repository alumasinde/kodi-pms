-- Only remove seeded rows that nothing references, so rollback never fails on FKs.
DELETE pm FROM payment_methods pm
WHERE pm.code IN ('mpesa_paybill','mpesa_till','mpesa_stk','bank_transfer','cash','cheque','card')
  AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.payment_method_id = pm.id);

DELETE a FROM amenities a
WHERE a.name IN ('Borehole','Backup Generator','Solar Water Heating','CCTV','24hr Security','Electric Fence','Parking',
                 'Lift','Gym','Swimming Pool','Fibre Internet','Balcony','Garbage Collection','Playground','Water Tank')
  AND NOT EXISTS (SELECT 1 FROM property_amenities x WHERE x.amenity_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM unit_amenities y WHERE y.amenity_id = a.id);

DELETE ut FROM unit_types ut
WHERE ut.name IN ('Single Room','Bedsitter','Studio','1 Bedroom','2 Bedroom','3 Bedroom','4 Bedroom','Maisonette','Shop','Office')
  AND NOT EXISTS (SELECT 1 FROM units u WHERE u.type_id = ut.id);

DELETE pt FROM property_types pt
WHERE pt.name IN ('Apartment Block','Bungalow','Maisonette','Townhouse','Commercial','Mixed Use','Hostel','Servant Quarters (DSQ)')
  AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.type_id = pt.id);
