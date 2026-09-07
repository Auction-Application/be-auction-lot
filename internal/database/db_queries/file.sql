-- name: CheckIdenticalBlobs :many
select image_blob_id,sha256 from public.image_blobs where is_upload_completed=true;