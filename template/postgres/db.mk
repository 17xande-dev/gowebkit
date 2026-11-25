# Database targets for the PostgreSQL variant, included by the shared Makefile.
SQLC_VERSION ?= v1.31.1
SQLC ?= $(GOBIN)/sqlc
POSTGRES_PORT ?= 5432
# Simply expanded, so every use in one recipe sees the same second; a recursive
# default would run date again for each name and could disagree across a tick.
BACKUP_STAMP := $(shell date -u +%Y%m%dT%H%M%SZ)
BACKUP_FILE ?= data/backups/app-$(BACKUP_STAMP).dump

.PHONY: sqlc sqlc-check sqlc-install db-up db-down psql backup restore
help: postgres-help
.PHONY: postgres-help
postgres-help:
	@printf '%s\n' 'Postgres: db-up db-down psql sqlc sqlc-check sqlc-install backup restore BACKUP_FILE=<path> CONFIRM=1'

# The tests need a database and must not skip without one.
test: export TEST_REQUIRE_DB := 1
test: export TEST_DATABASE_URL ?= postgres://app:app@127.0.0.1:$(POSTGRES_PORT)/app?sslmode=disable
# A database supplied in the environment is used as is;
# otherwise the compose database is started first.
ifneq ($(origin TEST_DATABASE_URL),environment)
test: db-up
endif

# Stale generated code is a gate failure, and it should fail before the slower
# steps, so it hangs off the first check rather than being listed after them.
fmt-check: sqlc-check

# Like govulncheck, sqlc installs itself the first time it is needed.
sqlc: $(SQLC)
	$(SQLC) generate

sqlc-check: $(SQLC)
	$(SQLC) diff

$(SQLC):
	GOBIN='$(dir $@)' go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

sqlc-install:
	GOBIN='$(dir $(SQLC))' go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

db-up:
	docker compose up --detach --wait postgres

# Removes the compose database and its volume. The gate never runs this, so a
# development database survives between runs; throwaway checks call it.
db-down:
	docker compose down -v

psql: db-up
	docker compose exec postgres psql -U app -d app

# A custom-format dump through the compose database, written on the host. It
# holds every account, so keep it private (umask 077) and out of version control.
backup: db-up
	@set -euo pipefail; umask 077; \
		mkdir -p -- "$(dir $(BACKUP_FILE))"; \
		docker compose exec -T postgres pg_dump -U app -d app --format=custom > "$(BACKUP_FILE).partial"; \
		docker compose exec -T postgres pg_restore --list < "$(BACKUP_FILE).partial" > /dev/null; \
		mv -- "$(BACKUP_FILE).partial" "$(BACKUP_FILE)"; \
		printf 'Backup saved and listed: %s\n' "$(BACKUP_FILE)"

# Replaces the compose development database with a dump. It checks the request
# and that the server container is stopped before it starts or touches anything.
restore: export CONFIRM := $(CONFIRM)
restore:
	@set -euo pipefail; \
		if [[ "$$CONFIRM" != 1 || ! -f "$(BACKUP_FILE)" ]]; then \
			printf '%s\n' 'Run make restore BACKUP_FILE=<dump> CONFIRM=1; this replaces the compose development database.' >&2; exit 1; \
		fi; \
		if [[ -n "$$(docker compose ps -q server)" ]]; then \
			printf '%s\n' 'Stop the server first (docker compose stop server): it would keep using the database being replaced.' >&2; exit 1; \
		fi; \
		docker compose up --detach --wait postgres; \
		docker compose exec -T postgres pg_restore -U app -d app --clean --if-exists --no-owner --single-transaction < "$(BACKUP_FILE)"; \
		printf 'Restored %s\n' "$(BACKUP_FILE)"
