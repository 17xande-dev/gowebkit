#!/usr/bin/env bash
# Called by the repository Makefile and, for generated apps, by gonewtest.
set -euo pipefail

export GOWORK=off

postgres_gate() {
	# Ask Docker to bind a free loopback port when it starts the service,
	# instead of finding a port and racing someone else to bind it.
	export POSTGRES_PORT=0
	make db-up
	local address
	address=$(docker compose port postgres 5432)
	TEST_DATABASE_URL="postgres://app:app@$address/app?sslmode=disable" TEST_REQUIRE_DB=1 make gate
}

app_gate() {
	if [[ $1 == postgres ]]; then
		postgres_gate
	else
		make gate
	fi
}

latest_modules() {
	local prefix=github.com/17xande-dev
	local -a modules=("$prefix/mailer@latest" "$prefix/password@latest"
		"$prefix/ratelimit@latest" "$prefix/websec@latest")
	if [[ $1 == sqlite ]]; then
		modules+=("$prefix/sqlitekit@latest")
	fi
	go get "${modules[@]}"
	go mod tidy
}

case ${1-} in
gate|latest-gate)
	# gonewtest owns the generated directory and its Compose teardown.
	if [[ $1 == latest-gate ]]; then
		latest_modules "$2"
	fi
	app_gate "$2"
	exit
	;;
check-all|check-all-fast|check-latest) ;;
*) printf 'Unknown local check: %s\n' "${1-}" >&2; exit 2 ;;
esac

# Do not use a development project's configuration or database. Generated
# gates inherit this environment and a project name owned by their run.
unset COMPOSE_PROJECT_NAME COMPOSE_FILE COMPOSE_ENV_FILES TEST_DATABASE_URL
export COMPOSE_DISABLE_ENV_FILE=1

script=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/local-check.sh
root=$(cd -- "$(dirname -- "$script")/.." && pwd)
cd "$root"
rev=$(git rev-parse --verify HEAD)
project="gowebkit-check-$$-$RANDOM"

generated() (
	local variant=$1 step=$2
	shift 2
	printf '\n==> Generated %s: %s (HEAD %s)\n' "$variant" "$step" "$rev"
	cd tools
	export COMPOSE_PROJECT_NAME="$project-$variant-generated"
	go run ./gonewtest -root .. -rev "$rev" -template "template/$variant" "$@" \
		-- bash "$script" "$step" "$variant"
)

if [[ $1 == check-latest ]]; then
	generated sqlite latest-gate
	generated postgres latest-gate
	exit
fi

printf '\n==> Source sqlite: gate\n'
(cd template/sqlite && make gate)
printf '\n==> Source postgres: gate\n'
(
	cd template/postgres
	export COMPOSE_PROJECT_NAME="$project-postgres-source"
	trap 'status=$?; make db-down || { if ((status == 0)); then status=1; fi; }; exit "$status"' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	postgres_gate
)

docker_build=()
if [[ $1 == check-all ]]; then
	docker_build=(-docker)
fi
generated sqlite gate "${docker_build[@]}"
generated postgres gate "${docker_build[@]}"

if [[ $1 == check-all ]]; then
	if [[ -n ${E2E_OUT-} ]]; then
		mkdir -p -- "$E2E_OUT"
		shots=$(cd -- "$E2E_OUT" && pwd)
	else
		shots=$(mktemp -d)
		trap 'rm -r -- "$shots"' EXIT
		trap 'exit 130' INT
		trap 'exit 143' TERM
	fi
	for variant in sqlite postgres; do
		printf '\n==> Headless first-run check: %s\n' "$variant"
		# The browser runner names its own stack with -p. Also isolate the
		# outer gonewtest teardown, which would otherwise default to "app".
		COMPOSE_PROJECT_NAME="$project-$variant-e2e" E2E_OUT="$shots/$variant" \
			bash tools/e2e/run.sh --variant "$variant" --rev "$rev"
	done
fi
