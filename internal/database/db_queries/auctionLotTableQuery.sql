-- name: CreateLot :one
insert into lots(title,description,category,bid_opening_price) values($1,$2,$3,$4) returning id;