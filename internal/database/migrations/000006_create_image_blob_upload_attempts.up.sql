create type  upload_attempt_state as enum('pending','committed');

create type upload_type as enum('multiUpload','singleUpload');

create table if not exists image_blob_upload_attempts(id bigint generated always as identity primary key,
sha256 bytea not null,
file_size bigint not null,
content_type text not null,
upload_type upload_type not null,

upload_id text,
part_size bigint,
part_count bigint generated always as (case
	when upload_type = 'multiUpload' then 
	(file_size + part_size-1)/ part_size
	else null
end) stored,

lot_id uuid not null references lots(id) on
delete
	cascade,
	filename varchar(250),
	storage_key text not null,
	username varchar(128) not null,
upload_state upload_attempt_state not null
);
