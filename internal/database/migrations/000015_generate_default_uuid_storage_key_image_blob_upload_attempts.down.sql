alter table image_blob_upload_attempts
alter storage_key type text using storage_key::text,
alter storage_key drop default;