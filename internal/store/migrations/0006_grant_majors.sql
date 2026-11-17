-- The app's compatibility line an access request is made for: MAJOR, or
-- 0.MINOR for a version 0.y.z (docs/specs/oauth.md, Requests). An approval
-- stamps it on each item it adds to the grant, inside the grants' items
-- JSON, and an item is in force only while the app is on that line. A
-- request made before this column has '', which no approval takes.
ALTER TABLE consent_requests ADD COLUMN major TEXT NOT NULL DEFAULT '';
