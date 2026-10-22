# ==================================================================================== #
# DEVELOPMENT
# ==================================================================================== #

## run/api: run the cmd/api application
.PHONY: run/api
run/api:
	go run ./cmd/api 

.PHONY: auto-run/api
auto-run/api:
	air

# ==================================================================================== #
# DATABASE MIGRATIONS
# ==================================================================================== #
.PHONY:migrate/db
migrate/db:
	migrate -path ./internal/database/migrations -database $(DATABASE_URL) up	


.PHONY:migrate/create
migrate/create:
	migrate create -seq -ext sql  -dir ./internal/database/migrations $(FILE_NAME)	


# ==================================================================================== #
# QUALITY CONTROL
# ==================================================================================== #

## tidy: format all .go files, and tidy and vendor module dependencies
.PHONY: tidy
tidy:
	go mod tidy
	go mod verify
	go mod vendor
	go fix ./...
	go fmt ./...

## audit: run quality control checkstidy
.PHONY: audit
audit:
	go mod tidy -diff
	go mod verify
	go vet ./...
	go tool staticcheck ./...
	go test -race -vet=off ./...

