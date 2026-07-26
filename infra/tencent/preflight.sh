#!/usr/bin/env bash
#
# Proves GitHub Actions can authenticate to the Tencent Cloud API before any
# provisioning work depends on it. Provisioning failures are expensive to debug
# because a bad credential and a missing CAM permission look identical from a
# `terraform apply` that dies half way through; this separates them up front.
#
#   ./preflight.sh --self-test   signing only, offline, no credentials needed
#   ./preflight.sh               live check against the API
#
# Reads TENCENTCLOUD_SECRET_ID / TENCENTCLOUD_SECRET_KEY from the environment,
# the same names the Terraform provider uses, so nothing has to be renamed when
# this graduates into real infrastructure.
#
# Note: openssl takes the derived key on its command line, which is readable via
# `ps` for the instant it runs. Fine on an ephemeral CI runner or a personal
# machine; do not run this on a host shared with people who should not have the
# key anyway.

set -euo pipefail

REGION="${TENCENTCLOUD_REGION:-ap-jakarta}"

sha256_hex() { printf '%s' "$1" | openssl dgst -sha256 -hex | sed 's/^.*= //'; }
hmac_hex() { printf '%s' "$2" | openssl dgst -sha256 -mac HMAC -macopt "hexkey:$1" -hex | sed 's/^.*= //'; }
to_hex() { printf '%s' "$1" | od -An -tx1 | tr -d ' \n'; }
utc_date() { date -u -r "$1" +%Y-%m-%d 2>/dev/null || date -u -d "@$1" +%Y-%m-%d; }
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

# SecretDate = HMAC_SHA256("TC3" + SecretKey, Date), then service, then the
# terminator — the scoping chain, so a captured signature is useless for another
# day or another product.
derive_signing_key() { # secret_key date service
  local k
  k=$(hmac_hex "$(to_hex "TC3$1")" "$2")
  k=$(hmac_hex "$k" "$3")
  hmac_hex "$k" "tc3_request"
}

# Split from the derivation above so the self-test can drive it with the signing
# key published in Tencent's worked example. Everything below this line is then
# checked against known-good values rather than assumed.
sign_with_key() { # signing_key host service action payload ts  -> canonical hashes on fd 3
  local signing_key=$1 host=$2 service=$3 action=$4 payload=$5 ts=$6
  local date_stamp canonical_request hashed_payload hashed_request

  date_stamp=$(utc_date "$ts")
  hashed_payload=$(sha256_hex "$payload")

  canonical_request=$(printf 'POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:%s\nx-tc-action:%s\n\ncontent-type;host;x-tc-action\n%s' \
    "$host" "$(lower "$action")" "$hashed_payload")
  hashed_request=$(sha256_hex "$canonical_request")

  printf '%s\n%s\n' "$hashed_payload" "$hashed_request" >&3

  hmac_hex "$signing_key" \
    "$(printf 'TC3-HMAC-SHA256\n%s\n%s/%s/tc3_request\n%s' "$ts" "$date_stamp" "$service" "$hashed_request")"
}

# Tencent's published worked example. If this passes, a live failure is the
# credentials; if it fails, it is this script — which is the whole point of
# running it before anything reaches the network.
#
# The docs redact the example SecretId/SecretKey but still publish the derived
# signing key and the resulting signature, so the vector pins canonicalisation,
# the string to sign, and the final HMAC. The three derivation steps above are
# the one part it cannot reach; a live AuthFailure.SignatureFailure while this
# passes would point there.
self_test() {
  local want_payload='35e9c5b0e3ae67532d3c9f17ead6c90222632e5b1ff7f6e89887f1398934f064'
  local want_request='7019a55be8395899b900fb5564e4200d984910f34794a27cb3fb7d10ff6a1e84'
  local want_sig='10b1a37a7301a02ca19a647ad722d5e43b4b3cff309d421d85b46093f6ab6c4f'
  local key='b596b923aad85185e2d1f6659d2a062e0a86731226e021e61bfe06f7ed05f5af'
  # The escapes stay literal: the vector signs the JSON exactly as it goes on
  # the wire, not its decoded form.
  local payload='{"Limit": 1, "Filters": [{"Values": ["\u672a\u547d\u540d"], "Name": "instance-name"}]}'
  local tmp got_sig got_payload got_request rc=0

  tmp=$(mktemp)
  got_sig=$(sign_with_key "$key" 'cvm.tencentcloudapi.com' 'cvm' 'DescribeInstances' "$payload" 1551113065 3>"$tmp")
  { read -r got_payload; read -r got_request; } < "$tmp"
  rm -f "$tmp"

  check() {
    if [ "$2" = "$3" ]; then
      printf '  ok    %s\n' "$1"
    else
      printf '  FAIL  %s\n        want %s\n        got  %s\n' "$1" "$3" "$2"
      rc=1
    fi
  }

  echo "Signature self-test (Tencent's published vector, offline):"
  check 'sha256(payload)'           "$got_payload" "$want_payload"
  check 'sha256(canonical request)' "$got_request" "$want_request"
  check 'signature'                 "$got_sig"     "$want_sig"
  return $rc
}

call() { # host service action version payload
  local host=$1 service=$2 action=$3 version=$4 payload=$5 ts stamp sig key

  ts=$(date -u +%s)
  stamp=$(utc_date "$ts")
  key=$(derive_signing_key "$TENCENTCLOUD_SECRET_KEY" "$stamp" "$service")
  sig=$(sign_with_key "$key" "$host" "$service" "$action" "$payload" "$ts" 3>/dev/null)

  curl -sS --max-time 20 "https://${host}/" \
    -H "Authorization: TC3-HMAC-SHA256 Credential=${TENCENTCLOUD_SECRET_ID}/${stamp}/${service}/tc3_request, SignedHeaders=content-type;host;x-tc-action, Signature=${sig}" \
    -H "Content-Type: application/json; charset=utf-8" \
    -H "Host: ${host}" \
    -H "X-TC-Action: ${action}" \
    -H "X-TC-Version: ${version}" \
    -H "X-TC-Timestamp: ${ts}" \
    -H "X-TC-Region: ${REGION}" \
    -d "$payload"
}

# Tencent answers HTTP 200 with an Error object in the body, so curl's exit
# status says nothing. The error code is what separates failures that need
# different fixes.
explain() {
  case "$1" in
    AuthFailure.SignatureFailure|AuthFailure.SignatureExpire)
      echo "        -> the signature or the clock, not the key. See the self-test note in this script." ;;
    AuthFailure.SecretIdNotFound)
      echo "        -> SecretId does not exist, or the key was disabled or deleted." ;;
    AuthFailure.*)
      echo "        -> credentials rejected. Re-copy the pair from CAM." ;;
    UnauthorizedOperation*)
      echo "        -> credentials are VALID but lack permission for this call." ;;
  esac
}

# Tencent runs two API partitions. An International account (tencentcloud.com)
# is not guaranteed to answer on the default hosts, and asking the wrong one
# returns AuthFailure — the same code a bad key returns. Try the other partition
# before blaming the credentials, and report which one answered, because
# everything downstream (Terraform, Ansible) has to point at the same place.
check_call() { # service action version
  local service=$1 action=$2 version=$3 out code host
  for host in "${service}.tencentcloudapi.com" "${service}.intl.tencentcloudapi.com"; do
    out=$(call "$host" "$service" "$action" "$version" '{}')
    code=$(printf '%s' "$out" | sed -n 's/.*"Code":"\([^"]*\)".*/\1/p')
    if [ -z "$code" ]; then
      ENDPOINT=$host
      printf '%s' "$out"
      return 0
    fi
    # Anything that is not an auth failure means the endpoint was right and the
    # problem is real. Retrying the other partition would only obscure it.
    case "$code" in AuthFailure*) ;; *) break ;; esac
  done
  printf '  FAIL  %s (both API partitions tried)\n' "$code" >&2
  explain "$code" >&2
  return 1
}

main() {
  if [ "${1:-}" = "--self-test" ]; then
    self_test
    return
  fi

  self_test || { echo "Signing is broken; not calling the API." >&2; return 1; }
  echo

  if [ -z "${TENCENTCLOUD_SECRET_ID:-}" ] || [ -z "${TENCENTCLOUD_SECRET_KEY:-}" ]; then
    cat >&2 <<'EOF'
TENCENTCLOUD_SECRET_ID / TENCENTCLOUD_SECRET_KEY are not set.

  Console -> Access Management -> API Keys, then add both as repository secrets
  under the same names. Grant the key QcloudCVMFullAccess and QcloudVPCFullAccess:
  that is what Terraform needs to create the instance and its security group.
EOF
    return 1
  fi

  local out state

  # Split deliberately. GetCallerIdentity needs no product permission, so it
  # answers "are these credentials real?" on its own; DescribeRegions then
  # answers "can they see CVM, and does this account have Jakarta?" A single
  # combined check would blame the key for what is really a CAM policy gap.
  echo "Identity (sts:GetCallerIdentity, needs no product permission):"
  out=$(check_call sts GetCallerIdentity 2018-08-13) || return 1
  printf '  ok    %s\n' "$(printf '%s' "$out" | sed -n 's/.*"Arn":"\([^"]*\)".*/\1/p')"
  printf '  ok    answered on %s\n' "$ENDPOINT"

  echo
  echo "Capability (cvm:DescribeRegions, is ${REGION} usable by this account?):"
  out=$(check_call cvm DescribeRegions 2017-03-12) || return 1

  state=$(printf '%s' "$out" | tr '{' '\n' | grep "\"Region\":\"${REGION}\"" | sed -n 's/.*"RegionState":"\([^"]*\)".*/\1/p')
  case "$state" in
    AVAILABLE) printf '  ok    %s is AVAILABLE\n' "$REGION" ;;
    "")        printf '  FAIL  %s is not in this account'"'"'s region list\n' "$REGION"; return 1 ;;
    *)         printf '  FAIL  %s is %s\n' "$REGION" "$state"; return 1 ;;
  esac
}

main "$@"
