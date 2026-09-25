ALTER TABLE pages ADD COLUMN status TEXT NOT NULL DEFAULT 'live'
    CHECK (status IN ('live', 'parking', 'parked', 'unparking'));
