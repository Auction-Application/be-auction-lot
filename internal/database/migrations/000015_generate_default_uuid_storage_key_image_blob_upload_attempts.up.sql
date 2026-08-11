alter table image_blob_upload_attempts 
alter storage_key type uuid using storage_key::uuid,
alter storage_key set default gen_random_uuid();