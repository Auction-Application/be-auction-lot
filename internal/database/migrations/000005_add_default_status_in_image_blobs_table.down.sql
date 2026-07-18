alter table public.image_blobs drop column is_upload_completed;

alter table public.image_blobs alter column sha256 drop not null;