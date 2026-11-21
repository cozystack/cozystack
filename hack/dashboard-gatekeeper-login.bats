#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The dashboard gatekeeper's login redirect, checked against the oauth2-proxy
# binary the chart ships rather than against the rendered config text: the
# chart is rendered for a non-default realm, the auth-proxy container runs with
# the image, args and alpha config from that render, and /oauth2/start must
# redirect to that realm with an allowed kc_idp_hint passed through. The image
# comes from the render, so bumping it re-runs these cases on the new binary.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
STUB_IMAGE="python:3.13-alpine@sha256:9ef1a599f863231c3d91e941389bfdc4a2d6070557243d54df1a4e57015b960e"
HOST=example.org
REALM=acme

setup() {
  strict_setup
  docker info >/dev/null 2>&1 || {
    echo "docker is required to run the gatekeeper image" >&2
    return 1
  }
  work=$(mktemp -d)
  mkdir -p "$work/config" "$work/client"
  printf 'unused' > "$work/client/client-secret"
}

teardown() {
  [ -n "${work:-}" ] || return 0
  for f in "$work"/cid-*; do
    if [ -e "$f" ]; then docker rm -f "$(cat "$f")" >/dev/null 2>&1 || true; fi
  done
  if [ -e "$work/network" ]; then docker network rm "$(cat "$work/network")" >/dev/null 2>&1 || true; fi
  rm -rf "$work"
}

# render <keycloak-internal-url>
render() {
  helm template dashboard "$REPO_ROOT/packages/system/dashboard" --namespace cozy-dashboard \
    --set-string "_cluster.root-host=$HOST" \
    --set-string '_cluster.oidc-enabled=true' \
    --set-string "_cluster.oidc-realm-name=$REALM" \
    --set-string "_cluster.keycloak-internal-url=$1" > "$work/rendered.yaml"
  yq 'select(.kind == "ConfigMap" and .metadata.name == "incloud-web-gatekeeper-config") | .data["alpha-config.yaml"]' \
    "$work/rendered.yaml" > "$work/config/alpha-config.yaml"
  yq 'select(.kind == "Deployment" and .metadata.name == "incloud-web-gatekeeper") | .spec.template.spec.containers[] | select(.name == "auth-proxy") | .image' \
    "$work/rendered.yaml" > "$work/image"
  yq 'select(.kind == "Deployment" and .metadata.name == "incloud-web-gatekeeper") | .spec.template.spec.containers[] | select(.name == "auth-proxy") | .args[]' \
    "$work/rendered.yaml" > "$work/args"
  [ -s "$work/config/alpha-config.yaml" ]
  [ -s "$work/image" ]
}

# start_proxy [docker run options...]; prints the published host:port
start_proxy() {
  local cookie_secret args=() a
  cookie_secret=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
  while IFS= read -r a; do
    args+=("${a/\$(OAUTH2_PROXY_COOKIE_SECRET)/$cookie_secret}")
  done < "$work/args"
  docker run -d "$@" -p 127.0.0.1::8000 \
    -v "$work/config:/etc/oauth2-proxy/config:ro" \
    -v "$work/client:/etc/oauth2-proxy/client:ro" \
    "$(cat "$work/image")" "${args[@]}" > "$work/cid-proxy"
  local addr
  addr=$(docker port "$(cat "$work/cid-proxy")" 8000/tcp | head -n1)
  timeout 60 sh -c "until curl -sf -o /dev/null http://$addr/ping; do sleep 0.5; done" || {
    echo "gatekeeper did not become ready; its log:" >&2
    docker logs "$(cat "$work/cid-proxy")" >&2 || true
    return 1
  }
  echo "$addr"
}

# login_redirect <host:port> <query>
login_redirect() {
  curl -sS -o /dev/null -w '%{redirect_url}' "http://$1/oauth2/start?$2"
}

auth_url="https://keycloak.$HOST/realms/$REALM/protocol/openid-connect/auth"

# expect_redirect <location> <must contain>... ; a leading "!" means must not
# contain. A function, not [[ ]]: a failing [[ ]] mid-test does not stop the
# test under bash 3.2.
expect_redirect() {
  local location=$1 want
  shift
  case "$location" in
    "$auth_url?"*) ;;
    *) echo "expected a redirect to $auth_url, got: ${location:-<none>}" >&2; return 1 ;;
  esac
  for want in "$@"; do
    case "$want" in
      !*)
        case "$location" in
          *"${want#!}"*) echo "unexpected ${want#!} in: $location" >&2; return 1 ;;
        esac
        ;;
      *)
        case "$location" in
          *"$want"*) ;;
          *) echo "expected $want in: $location" >&2; return 1 ;;
        esac
        ;;
    esac
  done
}

@test "in-cluster Keycloak URL: login redirects to the configured realm and passes an allowed kc_idp_hint" {
  render "http://keycloak-http.cozy-keycloak.svc:8080/realms/$REALM"
  addr=$(start_proxy --network bridge)

  expect_redirect "$(login_redirect "$addr" 'kc_idp_hint=portal')" '&kc_idp_hint=portal&'
  expect_redirect "$(login_redirect "$addr" 'kc_idp_hint=x%26prompt%3Dnone')" '!kc_idp_hint' '!prompt=none'
  expect_redirect "$(login_redirect "$addr" '')" '!kc_idp_hint'
}

@test "OIDC discovery: the gatekeeper discovers the configured realm and passes an allowed kc_idp_hint" {
  render ""
  issuer="https://keycloak.$HOST/realms/$REALM"

  mkdir -p "$work/stub/realms/$REALM/.well-known"
  cat > "$work/stub/realms/$REALM/.well-known/openid-configuration" <<EOF
{
  "issuer": "$issuer",
  "authorization_endpoint": "$auth_url",
  "token_endpoint": "$issuer/protocol/openid-connect/token",
  "userinfo_endpoint": "$issuer/protocol/openid-connect/userinfo",
  "jwks_uri": "$issuer/protocol/openid-connect/certs",
  "end_session_endpoint": "$issuer/protocol/openid-connect/logout",
  "response_types_supported": ["code"],
  "subject_types_supported": ["public"],
  "id_token_signing_alg_values_supported": ["RS256"]
}
EOF
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=keycloak.$HOST" \
    -addext "subjectAltName=DNS:keycloak.$HOST" \
    -keyout "$work/stub/tls.key" -out "$work/stub/tls.crt" 2>/dev/null

  network="gatekeeper-login-$(basename "$work" | tr -cd 'a-zA-Z0-9')"
  docker network create "$network" >/dev/null
  echo "$network" > "$work/network"
  docker run -d --network "$network" --network-alias "keycloak.$HOST" \
    -v "$work/stub:/stub:ro" -w /stub "$STUB_IMAGE" python3 -c '
import http.server, ssl
server = http.server.ThreadingHTTPServer(("", 443), http.server.SimpleHTTPRequestHandler)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("tls.crt", "tls.key")
server.socket = ctx.wrap_socket(server.socket, server_side=True)
server.serve_forever()
' > "$work/cid-stub"
  # Discovery runs once at startup and the gatekeeper exits if it fails.
  timeout 60 sh -c "until docker exec $(cat "$work/cid-stub") python3 -c 'import socket; socket.create_connection((\"127.0.0.1\", 443), 1)' 2>/dev/null; do sleep 0.5; done" || {
    docker logs "$(cat "$work/cid-stub")" >&2 || true
    false
  }

  addr=$(start_proxy --network "$network" -v "$work/stub/tls.crt:/stub-ca.crt:ro" -e SSL_CERT_FILE=/stub-ca.crt)

  expect_redirect "$(login_redirect "$addr" 'kc_idp_hint=portal')" '&kc_idp_hint=portal&'
}
