alter table public.image_blobs add column is_upload_completed bool not null default false;

alter table public.image_blobs alter column sha256 set not null;