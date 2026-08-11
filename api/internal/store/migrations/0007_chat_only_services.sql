-- Some packages are agreed, not booked.
--
-- A photographer session is quoted: the customer says where, for how long, how
-- many people, whether there is a second location, and the price follows from
-- the answers. The booking page cannot hold that conversation, and pretending it
-- can produces the worst outcome available — a confirmed slot against a price
-- nobody has agreed to, which the studio then has to walk back by phone.
--
-- So a service now says how it is sold. 'web' is the page: pick a time, get a
-- confirmation. 'chat' is a WhatsApp link and no slot at all — the package is
-- still listed, still priced where there is a price, and simply does not offer a
-- calendar. Availability and Book both refuse a 'chat' service, so a crafted
-- POST cannot reach around the page and take a slot the studio never sold.
--
-- A column rather than a table because it is one fact about one row, and rather
-- than deactivating the packages because `active = 0` would take them off the
-- price list entirely — the studio does still sell them, and a customer looking
-- for a photographer should find one.
ALTER TABLE booking_services
  ADD COLUMN booking_mode TEXT NOT NULL DEFAULT 'web'
  CHECK (booking_mode IN ('web', 'chat'));

-- The four that were never really bookable online: three off-site photographer
-- lengths and the videographer. Existing rows default to 'web' above, so this is
-- the only statement that has to name anything.
UPDATE booking_services
   SET booking_mode = 'chat'
 WHERE id IN ('photographer-1h', 'photographer-90m', 'photographer-3h', 'videographer-3h');

-- The resource stops being "luar studio" the moment a studio session joins it.
-- The id stays: it is referenced by every booking ever taken against it, and the
-- name is the only part anybody reads.
UPDATE booking_resources SET name = 'Fotografer' WHERE id = 'photographer';
