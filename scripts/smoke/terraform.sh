#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# terraform: publish a module and a provider with the publish API, then terraform init
# installs both (a TLS proxy gives the registry the https dotted host terraform requires).
# Image: TERRAFORM_IMAGE (default docker.io/hashicorp/terraform:latest).
# Usage: OA_TOKEN=... scripts/smoke/terraform.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init terraform docker.io/hashicorp/terraform:latest "$@"

# Terraform verifies the SHA256SUMS signature: give the repository its own signing key
# (publish phase only; the key outlives the run unless the mode is both and KEEP is not 1).
if [ "$SMOKE_MODE" != install ]; then
	key="$(smoke_api POST /api/v1/admin/gpg/keys/generate -H 'Content-Type: application/json' \
		--data "{\"name\":\"$SMOKE_REPO\",\"algorithm\":\"ed25519\"}" | jq -r '.id // .key.id // empty')"
	[ -n "$key" ] || die "generating the signing key failed"
	smoke_cleanup_all() {
		if [ "$SMOKE_MODE" = both ] && [ "${KEEP:-0}" != 1 ]; then
			smoke_api DELETE "/api/v1/admin/gpg/keys/$key?force=true" -o /dev/null || true
		fi
		smoke_cleanup
	}
	trap smoke_cleanup_all EXIT
	code="$(smoke_api PUT "/api/v1/admin/gpg/keys/$key/activate" -H 'Content-Type: application/json' \
		--data "{\"repository\":\"$SMOKE_REPO\"}" -o /dev/null -w '%{http_code}')"
	[ "$code" = 200 ] || [ "$code" = 204 ] || die "activating the signing key failed (HTTP $code)"
fi

smoke_run "$(cat <<'SCRIPT'
set -eu
terraform version | head -1
apk add -q caddy openssl zip curl >/dev/null
host=oa.smoke.test
port=$((20000 + $(date +%s) % 20000))
hp="$host:$port"
echo "127.0.0.1 $host" >> /etc/hosts
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$host" -addext "subjectAltName=DNS:$host" \
  -keyout /tmp/key.pem -out /tmp/cert.pem 2>/dev/null
export SSL_CERT_FILE=/tmp/cert.pem
cat > /tmp/Caddyfile <<EOF
{
	admin off
	auto_https off
}
https://$hp {
	tls /tmp/cert.pem /tmp/key.pem
	reverse_proxy ${OA_URL#*://}
}
EOF
caddy run --config /tmp/Caddyfile --adapter caddyfile >/tmp/caddy.log 2>&1 &
i=0
until curl -fsS "https://$hp/.well-known/terraform.json" >/dev/null 2>&1; do
  i=$((i+1)); [ $i -lt 30 ] || { cat /tmp/caddy.log; echo "TLS proxy did not start"; exit 1; }; sleep 1
done
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
bin="terraform-provider-${PKG}_v$VERSION"
[ "$SMOKE_MODE" = install ] || {
  auth="Authorization: Bearer $OA_TOKEN"
  mkdir -p /tmp/mod /tmp/prov
  printf 'variable "x" {}\noutput "marker" { value = "%s" }\n' "$PKG" > /tmp/mod/main.tf
  tar -czf /tmp/mod.tar.gz -C /tmp/mod .
  curl -fsS -X PUT -H "$auth" --data-binary @/tmp/mod.tar.gz "$OA_URL/repos/$REPO/_api/modules/$PKG/aws/$VERSION" >/dev/null
  echo "$PKG" > "/tmp/prov/$bin"
  zip -q -j /tmp/prov.zip "/tmp/prov/$bin"
  curl -fsS -X PUT -H "$auth" -H "X-Checksum-Sha256: $(sha256sum /tmp/prov.zip | cut -d' ' -f1)" \
    --data-binary @/tmp/prov.zip "$OA_URL/repos/$REPO/_api/providers/$PKG/$VERSION/linux/$arch" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  cat > /tmp/tfrc <<EOF
credentials "$hp" {
  token = "$OA_TOKEN"
}
EOF
  export TF_CLI_CONFIG_FILE=/tmp/tfrc
  # a fresh working directory: nothing initialised, no plugin cache
  mkdir -p /tmp/work && cd /tmp/work
  cat > main.tf <<EOF
terraform {
  required_providers {
    $PKG = {
      source  = "$hp/$REPO/$PKG"
      version = "$VERSION"
    }
  }
}
module "m" {
  source  = "$hp/$REPO/$PKG/aws"
  version = "$VERSION"
  x       = 1
}
EOF
  terraform init -no-color >/tmp/init.log 2>&1 || { cat /tmp/init.log; exit 1; }
  grep -rq "value = \"$PKG\"" .terraform/modules
  [ "$(cat ".terraform/providers/$hp/$REPO/$PKG/$VERSION/linux_$arch/$bin")" = "$PKG" ]
}
SCRIPT
)"
