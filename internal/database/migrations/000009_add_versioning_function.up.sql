create or replace function bump_row_version() 
returns trigger
language plpgsql
as 
$$
begin

if new.version <> old.version then
raise exception 'column "version" of table "%" is not user-updatable', TG_TABLE_NAME
using errcode='restrict_violation';
end if;

if new =old then
return new;
end if;

 new.version:=old.version+1;
return new;
end;
$$