create or replace trigger bump_row_version before update on image_blob_upload_attempts
for each row execute procedure bump_row_version();