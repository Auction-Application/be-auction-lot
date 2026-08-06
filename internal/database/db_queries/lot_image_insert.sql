-- name: InsertIdenticalImageBlobsToLotImages :many
WITH file_uploads
     (
          sha256,
          lot_id,
          file_name
     )
     AS
     (
            SELECT sha256,
                   file_name,
                   @lot_id::uuid
            FROM   unnest(@sha256s::   char(64)[]),
                   unnest(@file_names::varchar(250)[]) i(sha256,file_name)
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



-- name: InsertSinglePartUpload :exec
INSERT INTO image_blob_upload_attempts
            (
                        sha256,
                        file_size,
                        content_type,
                        upload_type,
                        lot_id,
                        file_name,
                        storage_key,
                        username
            )
SELECT Decode(i.sha256,'hex'),
       i.file_size,
       i.content_type,
       @upload_type::upload_type,
       @lot_id::     uuid,
       i.file_name,
       i.storage_key,
       @username::text
FROM   unnest( @sha256s::char(64)[]), unnest(@file_sizes::int[]), unnest(@content_types:: text[]), unnest(@files_names::text[]), unnest(@storage_keys::uuid[] ) i(sha256,file_size,content_type,file_name,storage_key)
ON conflict (sha256,lot_id)
WHERE  upload_state='pending' do nothing;



-- name: ListMultiUploadItems :many
SELECT
image_blob_upload_attempt_id,
  encode(sha256, 'hex') AS sha256,
  upload_id,
  valid_until,
  file_size,
  part_size,
  ARRAY(
    SELECT
      generate_series(1, part_count, 1)
  )::smallint[] AS parts
FROM
  image_blob_upload_attempts
WHERE
  sha256 = ANY (
    SELECT
      decode(i.sha256, 'hex') AS sha256
    FROM
      unnest(@sha256s::char(64)[]) i(sha256)
  )
  AND lot_id = @lot_id::uuid
  AND upload_type = 'multiUpload'::upload_type
  AND upload_state = 'pending'::upload_state
FOR UPDATE;


-- update  image_blob_upload_attempts iba
-- set upload_id=m.upload_id,
--  valid_until=default,
--  part_size=m.part_size
-- from unnest(@upload_ids::text[]),unnest(@image_blob_upload_attempt_ids::bigint[]),unnest(@part_sizes::bigint[]) 
-- m(upload_id,image_blob_upload_attempt_id,part_size) where iba.image_blob_upload_attempt_id=m.image_blob_upload_attempt_id;

-- name: ValidateMultipartUpload :exec
update  image_blob_upload_attempts iba
set upload_id=input.upload_id,
 valid_until=default,
 part_size=input.part_size
from
(
	select m.upload_id,m.image_blob_upload_attempt_id,@part_size::bigint from 
	unnest(@upload_ids::text[]),unnest(@image_blob_upload_attempt_ids::bigint[]) 
m(upload_id,image_blob_upload_attempt_id)
) as input

 where iba.image_blob_upload_attempt_id=input.image_blob_upload_attempt_id;




-- name: InsertNewMultipartUpload :many
insert into image_blob_upload_attempts(sha256,file_size,content_type,upload_type,upload_id,part_size,lot_id,file_name,storage_key,username)
select  decode(i.sha256, 'hex') sha256, i.file_size,i.content_type,@upload_type::upload_type,i.upload_id,i.part_size,@lot_id::uuid,i.file_name,i.storage_key,
@username::varchar(128) from
unnest(
@sha256s::char(64)[]),unnest(@file_sizes::int[]),unnest(@content_types::text[]),unnest(@upload_ids::text[]),unnest(@part_sizes::int[]),
unnest(@file_names::varchar(250)[]),unnest(@storage_keys::uuid[]
)
on conflict (sha256,lot_id) where upload_state='pending' do update 
set storage_key=image_blob_upload_attempts.storage_key returning sha256,storage_key,upload_id,(old is null)::boolean as is_inserted
;







