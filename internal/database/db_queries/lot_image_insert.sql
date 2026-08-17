-- name: InsertIdenticalImageBlobsToLotImages :many
WITH file_uploads
     (
          sha256,
          file_name,
          lot_id
     )
     AS
     (
            SELECT sha256,
                   file_name,
                   @lot_id::uuid
            FROM   unnest(@sha256s::char(64)[],@file_names::varchar(250)[]) i(sha256,file_name)
     )
     ,
     lot_image_inserts AS
     (
                 INSERT INTO lot_images
                             (
                                         lot_id,
                                         image_blob_id,
                                         file_name
                             )
                 SELECT file_uploads.lot_id,
                        image_blobs.image_blob_id,
                        file_uploads.file_name
                 FROM   file_uploads
                 JOIN   image_blobs
                 ON     image_blobs.sha256 = decode(file_uploads.sha256, 'hex')
                 WHERE  image_blobs.is_upload_completed returning lot_image_id,
                        file_name,
                        image_blob_id
     )
SELECT i.lot_image_id,
       i.file_name,
       i.image_blob_id,
       encode(ib.sha256,'hex') sha256
FROM   lot_image_inserts i
JOIN   image_blobs ib
ON     i.image_blob_id=ib.image_blob_id;



-- name: InsertSinglePartUpload :many
INSERT INTO image_blob_upload_attempts
            (
                        sha256,
                        file_size,
                        content_type,
                        upload_type,
                        lot_id,
                        file_name,
                        username
            )
SELECT Decode(i.sha256,'hex'),
       i.file_size,
       i.content_type,
       @upload_type::upload_type,
       @lot_id::     uuid,
       i.file_name,
       @username::text
FROM   unnest( @sha256s::char(64)[],@file_sizes::int[],@content_types:: text[],@files_names::text[]) i(sha256,file_size,content_type,file_name)
ON conflict (sha256,lot_id)
WHERE  upload_state='pending'
do update  set storage_key=image_blob_upload_attempts.storage_key returning encode(sha256, 'hex') sha256,storage_key,(old is null)::boolean as is_inserted;




-- name: InsertAndValidateMultiPartUpload :many
insert into image_blob_upload_attempts(
  sha256, file_size, content_type, upload_type, 
  upload_id, part_size, lot_id, file_name, 
  username, storage_key
) 
select 
  decode(i.sha256, 'hex') sha256, 
  i.file_size, 
  i.content_type, 
  @upload_type :: upload_type, 
  i.upload_id, 
  @part_size :: bigint, 
  @lot_id :: uuid, 
  i.file_name, 
  @username :: varchar(128), 
  i.storage_key 
from 
  unnest(
    @sha256s :: char(64) [], 
  @file_sizes :: int[], 
  @content_types :: text[], 
  @upload_ids :: text[], 
  
    @file_names :: varchar(250) []
  , 
  @storage_keys :: uuid[]) i(
    sha256, file_size, content_type, upload_id, 
    file_name, storage_key
  ) on conflict (sha256, lot_id) 
where 
  upload_state = 'pending' do 
update 
set 
  part_size = case when image_blob_upload_attempts.valid_until >= transaction_timestamp() then image_blob_upload_attempts.part_size else @part_size :: bigint end, 
  valid_until = case when image_blob_upload_attempts.valid_until >= transaction_timestamp() then image_blob_upload_attempts.valid_until else transaction_timestamp()+ interval '5 days' end 
returning 
  part_size, 
  image_blob_upload_attempt_id, 
  encode(sha256,'hex') sha256, 
  storage_key, 
  upload_id,
    ARRAY(
    SELECT
      generate_series(1, part_count, 1)
  )::smallint[] AS parts,
  (old is null):: boolean as is_inserted, 
coalesce(old.valid_until >=Now(),false)::boolean as is_valid;


-- name: GetMultiPartUploadItem :one
select upload_id,storage_key,part_count from image_blob_upload_attempts where image_blob_upload_attempt_id= sqlc.arg(multipart_attempt_id);








