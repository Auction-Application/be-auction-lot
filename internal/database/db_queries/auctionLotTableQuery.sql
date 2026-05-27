-- name: CreateLot :exec
insert into lots(title,description) values($1,$2);