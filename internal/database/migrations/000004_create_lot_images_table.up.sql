create table lot_images(
    lot_image_id bigint generated always as identity primary key,
    lot_id uuid not null references lots(id) on delete cascade,
    image_blob_id bigint not null references image_blobs(image_blob_id) on delete restrict,
    filename varchar(250) not null
)