#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Point-in-time recovery of the OpenArtifact database, end to end, with the
# tools CloudNativePG runs inside its pods: PostgreSQL from the CloudNativePG
# operand image, WAL archiving with barman-cloud-wal-archive, a base backup
# with barman-cloud-backup, and a recovery with barman-cloud-restore, a
# restore_command that calls barman-cloud-wal-restore, and recovery_target_time.
# The object store is a throwaway Garage node.
#
# What it proves (docs/operations/postgres-pitr.md):
#   1. WAL files and a base backup of the database land in the object store.
#   2. After the repositories are wiped from the database, a recovery to a time
#      before the wipe brings back the repositories, the artifact rows written
#      after the base backup, and an API token minted before the wipe.
#   3. A server started on the recovered database serves both artifacts with
#      their original bytes (the blobs were never touched: PITR restores the
#      database only).
# It does not run Kubernetes: the Cluster, ObjectStore and ScheduledBackup
# resources are the operator's wrapper around these same commands.
#
# Usage:
#   go build -o /tmp/openartifact ./cmd/server
#   OA_BIN=/tmp/openartifact scripts/pitr-smoke.sh
#
# Environment:
#   OA_BIN        path to the openartifact server binary (required)
#   PG_IMAGE      default ghcr.io/cloudnative-pg/postgresql:18
#   GARAGE_IMAGE  default docker.io/dxflrs/garage:v2.4.1
#   DOCKER        container CLI (default docker; podman works)
#   PG_PORT       host port of the database (default: first free port from 55430)
#   OA_PORT       host port of the server (default: first free port from 18090)
#   KEEP          1 keeps the containers and the work directory
#
# Needs docker (or podman), curl, jq and openssl on the host, and Internet
# access to pull the two images.
set -euo pipefail

die() { echo "pitr-smoke: $*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

OA_BIN="${OA_BIN:-}"
[ -n "$OA_BIN" ] && [ -x "$OA_BIN" ] || die "set OA_BIN to the openartifact server binary (go build ./cmd/server)"
DOCKER="${DOCKER:-docker}"
PG_IMAGE="${PG_IMAGE:-ghcr.io/cloudnative-pg/postgresql:18}"
GARAGE_IMAGE="${GARAGE_IMAGE:-docker.io/dxflrs/garage:v2.4.1}"
for c in "$DOCKER" curl jq openssl; do command -v "$c" >/dev/null || die "$c is required"; done

free_port() { # first port from $1 upwards nothing listens on
	local p="$1"
	while (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; do p=$((p + 1)); done
	echo "$p"
}
PG_PORT="${PG_PORT:-$(free_port 55430)}"
OA_PORT="${OA_PORT:-$(free_port 18090)}"

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/oa-pitr.XXXXXX")"
run="$$-$RANDOM"
net="oa-pitr-$run"
garage="$net-garage"
pg="$net-pg"
pg_restored="$net-pg-restored"
oa_pid=""
oa_url="http://127.0.0.1:$OA_PORT"

# Throwaway credentials, generated per run.
s3_key="GK$(openssl rand -hex 12)"
s3_secret="$(openssl rand -hex 32)"
pg_password="$(openssl rand -hex 16)"
secret_key="$(openssl rand -hex 32)"
bucket=pitr
endpoint="http://$garage:3900"

cleanup() {
	local rc=$?
	[ -z "$oa_pid" ] || kill "$oa_pid" 2>/dev/null || true
	if [ "${KEEP:-0}" = 1 ]; then
		echo "KEEP=1: containers $garage $pg $pg_restored and $work are left in place"
		return $rc
	fi
	"$DOCKER" rm -f "$garage" "$pg" "$pg_restored" >/dev/null 2>&1 || true
	"$DOCKER" network rm "$net" >/dev/null 2>&1 || true
	rm -rf "$work"
	return $rc
}
trap cleanup EXIT

# barman-cloud connection flags, shared by every command run in the database image.
barman_flags="--endpoint-url $endpoint"
bucket_url="s3://$bucket"

psql_in() { # CONTAINER DATABASE SQL
	"$DOCKER" exec -e PGPASSWORD="$pg_password" -e PGOPTIONS='-c client_min_messages=warning' "$1" psql -h 127.0.0.1 -U postgres -d "$2" -v ON_ERROR_STOP=1 -Atqc "$3"
}

wait_for() { # SECONDS DESCRIPTION COMMAND...
	local deadline=$((SECONDS + $1)) what="$2"
	shift 2
	until "$@" >/dev/null 2>&1; do
		[ "$SECONDS" -lt "$deadline" ] || die "timed out waiting for $what"
		sleep 0.5
	done
}

start_pg_container() { # NAME SCRIPT  (a file in $work/scripts, run by bash inside the image)
	"$DOCKER" run -d --name "$1" --network "$net" -p "127.0.0.1:$PG_PORT:5432" \
		--tmpfs /pgdata:rw,mode=1777,size=1g \
		-v "$work/scripts:/scripts:ro" \
		-e AWS_ACCESS_KEY_ID="$s3_key" -e AWS_SECRET_ACCESS_KEY="$s3_secret" -e AWS_DEFAULT_REGION=garage \
		-e PG_PASSWORD="$pg_password" \
		--entrypoint bash "$PG_IMAGE" "/scripts/$2" >/dev/null
}

pg_ready() { psql_in "$1" postgres 'select 1'; }

# --- the server ---------------------------------------------------------------

start_oa() {
	: >"$work/oa.log"
	DB_HOST=127.0.0.1 DB_PORT="$PG_PORT" DB_USER=postgres DB_PASSWORD="$pg_password" \
		DB_NAME=openartifact DB_SSLMODE=disable SERVER_PORT="$OA_PORT" \
		STORAGE_BACKEND=filesystem STORAGE_PATH="$work/blobs" \
		OPENARTIFACT_SECRET_KEY="$secret_key" BASE_URL="$oa_url" \
		"$OA_BIN" >"$work/oa.log" 2>&1 &
	oa_pid=$!
	wait_for 90 "the server to answer /health" curl -fsS "$oa_url/health"
}

stop_oa() {
	[ -z "$oa_pid" ] || { kill "$oa_pid" 2>/dev/null || true; wait "$oa_pid" 2>/dev/null || true; }
	oa_pid=""
}

api() { # METHOD PATH [curl args...]
	local method="$1" path="$2"
	shift 2
	curl -fsS -X "$method" "$oa_url$path" -H "Authorization: Bearer $token" "$@"
}

sha() { sha256sum | cut -d' ' -f1; }

# --- 1. object store ----------------------------------------------------------

step "start the object store (Garage) and a network"
mkdir -p "$work/blobs"
"$DOCKER" network create "$net" >/dev/null
cat >"$work/garage.toml" <<EOF
metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "$(openssl rand -hex 32)"
[s3_api]
s3_region = "garage"
api_bind_addr = "[::]:3900"
root_domain = ".s3.garage.localhost"
[admin]
api_bind_addr = "[::]:3903"
EOF
"$DOCKER" run -d --name "$garage" --network "$net" -v "$work/garage.toml:/etc/garage.toml:ro" "$GARAGE_IMAGE" >/dev/null
g() { "$DOCKER" exec "$garage" /garage "$@" 2>>"$work/garage-cli.log"; }
wait_for 60 "Garage" g status
node="$(g node id -q | cut -d@ -f1)"
g layout assign -z dc1 -c 1G "$node" >/dev/null
g layout apply --version 1 >/dev/null
g bucket create "$bucket" >/dev/null
g key import --yes -n pitr "$s3_key" "$s3_secret" >/dev/null
g bucket allow --read --write --owner "$bucket" --key pitr >/dev/null
echo "bucket $bucket ready on $garage"

# --- 2. database with WAL archiving -------------------------------------------

step "start PostgreSQL ($PG_IMAGE) with WAL archiving to the object store"
# archive_timeout bounds the time a quiet database keeps a WAL segment unarchived.
mkdir -p "$work/scripts"
cat >"$work/scripts/init.sh" <<EOF
set -e
printf '%s' "\$PG_PASSWORD" >/tmp/pw
initdb -D /pgdata/data -U postgres --pwfile=/tmp/pw --auth-local=trust --auth-host=scram-sha-256 >/dev/null
echo "host all all 0.0.0.0/0 scram-sha-256" >>/pgdata/data/pg_hba.conf
cat >>/pgdata/data/postgresql.conf <<CONF
listen_addresses = '*'
wal_level = replica
archive_mode = on
archive_timeout = 10s
archive_command = 'barman-cloud-wal-archive $barman_flags $bucket_url oa-pitr %p'
CONF
exec postgres -D /pgdata/data
EOF
start_pg_container "$pg" init.sh
wait_for 60 "PostgreSQL" pg_ready "$pg"
psql_in "$pg" postgres 'create database openartifact'
psql_in "$pg" postgres 'show archive_command'

# --- 3. the application writes data ------------------------------------------

step "start the server on the database, create a token, a repository and artifact one"
start_oa
pass="$(grep -A1 'OPENARTIFACT BOOTSTRAP' "$work/oa.log" | sed -n 's/^ *admin password: *//p')"
[ -n "$pass" ] || die "bootstrap admin password not found in the server output"
token="$(OA_URL="$oa_url" OA_PASS="$pass" OA_NEW_PASS="$(openssl rand -hex 16)" "$here/dev-token.sh" --change-password)"
[ -n "$token" ] || die "no token"
api POST /api/v1/repositories -H 'Content-Type: application/json' \
	-d '{"name":"releases","format":"raw","public":false}' >/dev/null
printf 'artifact one, written before the base backup\n' >"$work/one.txt"
api PUT /repos/raw/releases/one.txt --data-binary @"$work/one.txt" >/dev/null
echo "uploaded one.txt sha256=$(sha <"$work/one.txt")"

step "take a base backup (barman-cloud-backup)"
"$DOCKER" exec -e PGPASSWORD="$pg_password" "$pg" \
	barman-cloud-backup $barman_flags -h 127.0.0.1 -U postgres "$bucket_url" oa-pitr
"$DOCKER" exec "$pg" barman-cloud-backup-list $barman_flags --format json "$bucket_url" oa-pitr |
	jq -r '.backups_list[] | "backup \(.backup_id) status=\(.status)"'
backup_id="$("$DOCKER" exec "$pg" barman-cloud-backup-list $barman_flags --format json "$bucket_url" oa-pitr |
	jq -r '.backups_list[-1].backup_id')"
[ -n "$backup_id" ] && [ "$backup_id" != null ] || die "no backup in the object store"

step "write artifact two after the base backup"
printf 'artifact two, written after the base backup\n' >"$work/two.txt"
api PUT /repos/raw/releases/two.txt --data-binary @"$work/two.txt" >/dev/null
echo "uploaded two.txt sha256=$(sha <"$work/two.txt")"
stop_oa

# --- 4. the disaster ----------------------------------------------------------

# Recovery stops at the first transaction after this time, so it has to be a
# moment strictly before the wipe and the wipe has to be archived.
target="$(psql_in "$pg" postgres "select to_char(now() at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS.US') || '+00'")"
step "recovery target: $target"
repos_before="$(psql_in "$pg" openartifact 'select count(*) from repositories')"
psql_in "$pg" openartifact 'truncate repositories cascade' >/dev/null
repos_wiped="$(psql_in "$pg" openartifact 'select count(*) from repositories')"
echo "repositories before=$repos_before after the wipe=$repos_wiped"
[ "$repos_before" -ge 1 ] && [ "$repos_wiped" = 0 ] || die "the wipe did not happen as expected"

# Push the segment holding the wipe into the archive: PITR needs WAL after the target.
last="$(psql_in "$pg" postgres 'select pg_walfile_name(pg_switch_wal() - 1)')"
archived() { # WAL names are fixed-width hex, so string order is WAL order
	local cur
	cur="$(psql_in "$pg" postgres 'select last_archived_wal from pg_stat_archiver')"
	[[ "$cur" == "$last" || "$cur" > "$last" ]]
}
wait_for 60 "the WAL segment $last to be archived" archived
echo "archiver: $(psql_in "$pg" postgres "select 'last_archived_wal=' || last_archived_wal || ' archived_count=' || archived_count || ' failed_count=' || failed_count from pg_stat_archiver")"
"$DOCKER" rm -f "$pg" >/dev/null
echo "source database destroyed"

# --- 5. recovery --------------------------------------------------------------

step "recover into a fresh instance: barman-cloud-restore + recovery_target_time=$target"
# Same sequence as the CloudNativePG recovery job: restore the base backup into an
# empty PGDATA, then start PostgreSQL with recovery.signal, a restore_command that
# pulls WAL from the archive and the recovery target. The new instance archives
# under another server name so it never writes into the old timeline's namespace.
cat >"$work/scripts/restore.sh" <<EOF
set -e
mkdir -m 0700 /pgdata/data
barman-cloud-restore $barman_flags $bucket_url oa-pitr $backup_id /pgdata/data
touch /pgdata/data/recovery.signal
cat >>/pgdata/data/postgresql.auto.conf <<CONF
restore_command = 'barman-cloud-wal-restore $barman_flags $bucket_url oa-pitr %f %p'
recovery_target_time = '$target'
recovery_target_action = 'promote'
archive_command = 'barman-cloud-wal-archive $barman_flags $bucket_url oa-pitr-r1 %p'
CONF
exec postgres -D /pgdata/data
EOF
recovery_started=$SECONDS
start_pg_container "$pg_restored" restore.sh
wait_for 180 "PostgreSQL to finish recovery and promote" pg_ready "$pg_restored"
recovered() { [ "$(psql_in "$pg_restored" postgres 'select pg_is_in_recovery()')" = f ]; }
wait_for 180 "the instance to leave recovery" recovered
echo "recovery (base backup restore + WAL replay + promote) took $((SECONDS - recovery_started))s"
"$DOCKER" logs "$pg_restored" 2>&1 | grep -E "recovery stopping|redo done|starting point-in-time recovery" | sed 's/^/  /' || true

step "verify the recovered database"
repos_after="$(psql_in "$pg_restored" openartifact 'select count(*) from repositories')"
echo "repositories after recovery=$repos_after (want $repos_before)"
[ "$repos_after" = "$repos_before" ] || die "repositories were not recovered"

start_oa
list="$(api GET /api/v1/repositories | jq -r '[.. | objects | select(has("name")) | .name] | join(",")')"
echo "repositories served: $list"
case ",$list," in *,releases,*) ;; *) die "repository releases is not served after recovery" ;; esac
for f in one two; do
	want="$(sha <"$work/$f.txt")"
	got="$(curl -fsS -H "Authorization: Bearer $token" "$oa_url/repos/raw/releases/$f.txt" | sha)"
	echo "$f.txt sha256 served=$got"
	[ "$got" = "$want" ] || die "$f.txt differs after recovery"
done
echo "the API token minted before the wipe authorised those requests"
stop_oa

step "PASS: database recovered to $target, both artifacts served with their original bytes"
