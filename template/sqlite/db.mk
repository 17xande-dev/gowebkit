.PHONY: recreate-db backup-now
help: sqlite-help
.PHONY: sqlite-help
sqlite-help:
	@printf '%s\n' 'SQLite:   recreate-db CONFIRM=1 (default local file only), backup-now (requires sqlite3)'

recreate-db: export CONFIRM := $(CONFIRM)
recreate-db:
	@set -euo pipefail; \
		if [[ "$$CONFIRM" != 1 || "$${DB_PATH-data/app.db}" != data/app.db || -L data ]]; then \
			printf '%s\n' 'Stop the local app first, then run make recreate-db CONFIRM=1 for data/app.db only.' >&2; exit 1; \
		fi; \
		go run . -check-config; \
		rm -f -- data/app.db data/app.db-wal data/app.db-shm; \
		go run . -migrate

backup-now:
	@set -euo pipefail; umask 077; \
		db="$${DB_PATH-data/app.db}"; dir="$${BACKUP_DIR-$$(dirname -- "$$db")/backups}"; \
		[[ -f "$$db" ]] || { printf '%s\n' 'Database file not found.' >&2; exit 1; }; \
		[[ "$$db" = /* ]] || db="$$PWD/$$db"; \
		[[ -z "$$dir" || "$$dir" = /* ]] || dir="$$PWD/$$dir"; \
		mkdir -p -- "$$dir"; \
		tmp=$$(mktemp -d "$$dir/.manual-$$(date -u +%Y%m%dT%H%M%SZ)-XXXXXXXX"); \
		trap 'rm -f -- "$$tmp/backup.db"; rmdir -- "$$tmp"' EXIT; \
		escaped=$${tmp//\'/\'\'}; \
		sqlite3 -batch -bail -readonly -cmd '.timeout 5000' "$$db" "VACUUM INTO '$$escaped/backup.db'"; \
		name=$${tmp##*/}; dest="$$dir/$${name#.}.db"; \
		mv -- "$$tmp/backup.db" "$$dest"; \
		printf 'Backup saved: %s\n' "$$dest"
