#!/usr/bin/env bash
# Verify every container image referenced by a Game Spec — the bundled catalog
# (internal/panel/catalog/bundled/) and the dev seed specs (specs/) — can be
# pulled ANONYMOUSLY from its registry.
#
# Why anonymous: the Agent pulls with no registry credentials
# (internal/agent/docker.go pullImage), so a spec that names an unpublished or
# private image provisions fine on a node that happens to hold it locally and
# fails with a confusing registry "denied" everywhere else. This is the gate
# that keeps that class of failure out of shipped specs. Note GHCR creates a
# brand-new package PRIVATE on first push — flipping it public is a one-time
# manual step in the GitHub Packages UI, and this check stays red until then.
#
# Needs: bash, curl, python3 (JSON parsing). No docker daemon required.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ACCEPT='application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json'

# One HEAD against the registry's manifest endpoint, following the standard
# WWW-Authenticate token dance on 401 (GHCR and Docker Hub hand anonymous
# tokens out this way; MCR answers without one).
check_ref() { # <ref> → 0 pullable / 1 not
  local ref="$1" host rest tag auth code hdr realm service token

  host="${ref%%/*}"
  rest="${ref#*/}"
  case "$host" in
    *.* | *:* | localhost) : ;;
    *) host="registry-1.docker.io"; rest="$ref" ;; # bare "img:tag" → Docker Hub
  esac
  case "$rest" in
    */*) : ;;
    *) rest="library/$rest" ;;
  esac
  tag="latest"
  case "$rest" in
    *:*) tag="${rest##*:}"; rest="${rest%:*}" ;;
  esac

  local url="https://$host/v2/$rest/manifests/$tag"

  code=$(curl -sS -o /dev/null -w '%{http_code}' -I -H "Accept: $ACCEPT" "$url") || return 1
  if [ "$code" = "200" ]; then return 0; fi
  if [ "$code" != "401" ]; then return 1; fi

  # 401 → ask the advertised token endpoint for an anonymous pull token.
  hdr=$(curl -sS -I "$url" | tr -d '\r' | grep -i '^www-authenticate:' | head -n1) || return 1
  realm=$(printf '%s' "$hdr" | sed -n 's/.*realm="\([^"]*\)".*/\1/p')
  service=$(printf '%s' "$hdr" | sed -n 's/.*service="\([^"]*\)".*/\1/p')
  [ -n "$realm" ] || return 1

  token=$(curl -sS "$realm?service=$service&scope=repository:$rest:pull" |
    python3 -c 'import sys, json; d = json.load(sys.stdin); print(d.get("token") or d.get("access_token") or "")') || return 1
  [ -n "$token" ] || return 1

  code=$(curl -sS -o /dev/null -w '%{http_code}' -I -H "Accept: $ACCEPT" \
    -H "Authorization: Bearer $token" "$url") || return 1
  [ "$code" = "200" ]
}

# Every image: value in the spec documents — flow style ("{ kind: …, image: X }")
# and block style ("image: X") both reduce to the same token after the strip.
refs=$(grep -rhoE 'image:[[:space:]]*[A-Za-z0-9][A-Za-z0-9./_:@-]*' \
  "$ROOT/specs" "$ROOT/internal/panel/catalog/bundled" |
  sed -E 's/^image:[[:space:]]*//' | sort -u)

if [ -z "$refs" ]; then
  echo "no image references found — the grep is broken, not the specs" >&2
  exit 1
fi

fail=0
for ref in $refs; do
  if check_ref "$ref"; then
    echo "ok        $ref"
  else
    echo "MISSING   $ref  (not anonymously pullable — unpublished, private, or a bad tag)"
    fail=1
  fi
done
exit $fail
