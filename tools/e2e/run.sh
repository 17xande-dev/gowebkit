#!/usr/bin/env bash
# Run the first-run browser check (firstrun.mjs) against a generated app.
#
#   bash tools/e2e/run.sh [--variant sqlite|postgres] [--rev REV]
#       generate the app from a committed revision with the real gonew
#       (tools/gonewtest), start its compose stack on free ports, run the
#       check, and tear everything down.
#   bash tools/e2e/run.sh --app DIR
#       the same, in an already generated app directory.
#   bash tools/e2e/run.sh --url BASE_URL --mailpit URL [--logs-cmd CMD]
#       check a stack that is already running (for example `make up`); nothing
#       is started or stopped. The setup token is read with the logs command
#       (default: docker compose logs --no-color server, run in the current
#       directory) unless E2E_SETUP_TOKEN is set.
#
# Environment: E2E_OUT (screenshots and browser logs), E2E_CHROME, E2E_NO_SANDBOX=1.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
variant=sqlite rev=HEAD app="" url="" mailpit="" logs_cmd=""

while (($#)); do
	case "$1" in
	--variant) variant=$2; shift 2 ;;
	--rev) rev=$2; shift 2 ;;
	--app) app=$2; shift 2 ;;
	--url) url=$2; shift 2 ;;
	--mailpit) mailpit=$2; shift 2 ;;
	--logs-cmd) logs_cmd=$2; shift 2 ;;
	*) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

free_port() {
	node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})'
}

if [[ -n $url ]]; then
	[[ -n $mailpit ]] || { printf '%s\n' '--url needs --mailpit' >&2; exit 2; }
	E2E_BASE_URL=$url E2E_MAILPIT_URL=$mailpit ${logs_cmd:+E2E_LOGS_CMD=$logs_cmd} \
		exec node "$here/firstrun.mjs"
fi

if [[ -z $app ]]; then
	case "$variant" in sqlite | postgres) ;; *) printf 'unknown variant: %s\n' "$variant" >&2; exit 2 ;; esac
	# gonewtest runs the command inside the generated app and removes the app after.
	cd "$root/tools"
	GOWORK=off go run ./gonewtest -root "$root" -rev "$rev" -template "template/$variant" \
		-module example.com/app -- bash -c 'exec bash "$0" --app "$PWD"' "$here/run.sh"
	exit
fi

cd "$app"
[[ -f compose.yaml ]] || { printf 'no compose.yaml in %s\n' "$app" >&2; exit 2; }

scratch=$(mktemp -d)
project="gowebkit-e2e-$$-$RANDOM"
web=$(free_port)
mail=$(free_port)
export POSTGRES_PORT=$(free_port) # only the Postgres compose file reads it

compose() { docker compose -p "$project" -f compose.yaml -f "$scratch/override.yaml" "$@"; }
cleanup() {
	status=$?
	if [[ -f $scratch/override.yaml ]]; then
		compose down --volumes --remove-orphans --rmi local >/dev/null 2>&1 || true
	fi
	rm -rf "$scratch"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# BASE_URL must name the port the browser uses, or request-forgery checks refuse every form.
cat >"$scratch/override.yaml" <<YAML
services:
  server:
    ports: !override
      - "127.0.0.1:$web:8080"
    environment:
      BASE_URL: http://localhost:$web
  mailpit:
    ports: !override
      - "127.0.0.1:$mail:8025"
YAML

printf 'e2e: project %s, app on :%s, mail on :%s\n' "$project" "$web" "$mail"
compose up --build --detach --wait

for _ in $(seq 1 60); do
	curl -fsS "http://localhost:$web/healthz" >/dev/null 2>&1 && break
	sleep 1
done

E2E_BASE_URL="http://localhost:$web" E2E_MAILPIT_URL="http://localhost:$mail" \
	E2E_LOGS_CMD="$(printf '%q ' docker compose -p "$project" -f compose.yaml -f "$scratch/override.yaml" logs --no-color server)" \
	node "$here/firstrun.mjs"
