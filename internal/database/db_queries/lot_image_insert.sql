-- name: InsertIdenticalImageBlobsToLotImages :many
with file_uploads(sha256, lot_id, filename) as (
select
unnest(@sha256s::char(64)[]),
unnest(@lotIds::uuid[]),
unnest(@fileNames::varchar(250)[])
	),
 lot_image_inserts as (
insert
	into
		lot_images(lot_id, image_blob_id, filename)
		select
			file_uploads.lot_id,
			image_blobs.image_blob_id,
			file_uploads.filename
		from
			file_uploads
		join 
 image_blobs on
			image_blobs.sha256 = decode(file_uploads.sha256, 'hex')
		where
			image_blobs.is_upload_completed returning lot_image_id,
			filename,
			image_blob_id)
 
 select i.lot_image_id,i.filename,i.image_blob_id,encode(ib.sha256,'hex') sha256 from lot_image_inserts i join image_blobs ib on i.image_blob_id=ib.image_blob_id;