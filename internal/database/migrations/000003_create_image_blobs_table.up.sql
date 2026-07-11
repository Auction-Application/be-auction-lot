create table if not exists image_blobs(
    image_blob_id bigint generated always as identity primary key,
    sha256 bytea,
    content_type text not null,
    s3_key text not null,
    unique(sha256)

)