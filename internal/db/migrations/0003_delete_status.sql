ALTER TABLE pages DROP CONSTRAINT pages_status_check;
ALTER TABLE pages ADD CONSTRAINT pages_status_check
    CHECK (status IN ('live', 'parking', 'parked', 'unparking', 'deleting'));
