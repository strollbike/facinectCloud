-- Proximity sport list reads app_facility_sports. Onboarding used to write
-- only facility_sports, so the app kept sport id 0 and never requested slots.

INSERT INTO app_facility_sports (facility_id, sport_id)
SELECT DISTINCT fs.facility_id, fs.catalog_sport_id
FROM facility_sports fs
JOIN app_sports s ON s.id = fs.catalog_sport_id
JOIN app_facilities f ON f.id = fs.facility_id
WHERE fs.status = 'active'
  AND fs.catalog_sport_id IS NOT NULL
  AND fs.catalog_sport_id > 0
ON CONFLICT DO NOTHING;

INSERT INTO app_facility_sports (facility_id, sport_id)
SELECT DISTINCT fs.facility_id, s.id
FROM facility_sports fs
JOIN app_facilities f ON f.id = fs.facility_id
JOIN app_sports s ON lower(s.name) = lower(fs.name)
WHERE fs.status = 'active'
  AND COALESCE(fs.catalog_sport_id, 0) = 0
ON CONFLICT DO NOTHING;

INSERT INTO app_facility_sports (facility_id, sport_id)
SELECT DISTINCT c.facility_id, c.sport_id
FROM booking_courts c
JOIN app_sports s ON s.id = c.sport_id
JOIN app_facilities f ON f.id = c.facility_id
WHERE c.status = 'active'
  AND c.sport_id > 0
  AND NOT EXISTS (
    SELECT 1 FROM app_facility_sports existing
    WHERE existing.facility_id = c.facility_id
  )
ON CONFLICT DO NOTHING;
