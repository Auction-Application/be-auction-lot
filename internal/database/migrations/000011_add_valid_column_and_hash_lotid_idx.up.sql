create unique index idx_unq_img_attmpts_sha256_lot_id_upload_state on image_blob_upload_attempts(sha256,lot_id) where upload_state='pending';

alter table image_blob_upload_attempts add column valid_until timestamptz default current_timestamp + interval '5 days';
