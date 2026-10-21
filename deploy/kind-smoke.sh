#!/usr/bin/env bash
# Installs the Helm chart on a throwaway kind cluster the way a client deploys Popcorn (an image
# with the config baked in, a Secret, a volume), then checks it serves, keeps its data across
# restarts, and upgrades to a new config. Run from the repo root; CI runs it too.
#
#   deploy/kind-smoke.sh            # uses the cluster if it exists, else creates it and deletes it after
#   KEEP=1 deploy/kind-smoke.sh     # leaves a cluster it created running to poke at
#
# Needs docker, kind, kubectl and helm. Makes no paid calls: no API keys are set.
set -euo pipefail

CLUSTER=${CLUSTER:-popcorn-smoke}
PORT=${PORT:-3999}
TOKEN=smoke-token-$$
NS=popcorn-smoke
work=$(mktemp -d "${TMPDIR:-/tmp}/popcorn-smoke.XXXXXX")
pf=""
created="" # only a cluster this run created is deleted afterwards

cleanup() {
	if [ -n "$pf" ]; then kill "$pf" 2>/dev/null || true; fi
	rm -rf "$work"
	if [ -n "$created" ] && [ "${KEEP:-}" = "" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

say() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

say "cluster"
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
	kind create cluster --name "$CLUSTER" --wait 120s
	created=1
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

say "images: the app, and a deployment's image with the example config baked in"
docker build -q --build-arg VERSION=v0.0.0-smoke -t popcorn:smoke . >/dev/null
config_image() { # tag, config dir
	printf 'FROM popcorn:smoke\nCOPY . /config\nENV POPCORN_CONFIG_REV=%s\n' "$1" >"$work/Dockerfile"
	docker build -q -f "$work/Dockerfile" -t "popcorn-config:$1" "$2" >/dev/null
	kind load docker-image "popcorn-config:$1" --name "$CLUSTER" >/dev/null
}
config_image v1 examples/popcorn-config
nsets=$(find examples/popcorn-config/prompt-sets -name '*.toml' | wc -l | tr -d ' ')

say "install"
kubectl -n "$NS" create secret generic popcorn --from-literal=POPCORN_TOKENS="$TOKEN" \
	--dry-run=client -o yaml | kubectl apply -f - >/dev/null
install() { # tag
	helm upgrade --install popcorn charts/popcorn -n "$NS" --wait --timeout 180s \
		--set image.repository=popcorn-config --set image.tag="$1" --set image.pullPolicy=Never \
		--set existingSecret=popcorn >/dev/null
}
install v1

forward() {
	if [ -n "$pf" ]; then kill "$pf" 2>/dev/null || true; fi
	kubectl -n "$NS" port-forward svc/popcorn "$PORT:3000" >/dev/null 2>&1 &
	pf=$!
	for _ in $(seq 1 30); do
		if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then return 0; fi
		sleep 1
	done
	fail "no answer on port $PORT"
}
api() { curl -fsS -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT$1"; }
field() { # json key: a top-level string or number
	sed -n "s/.*\"$1\": *\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p" | head -1
}

say "serves"
forward
[ "$(curl -fsS "http://127.0.0.1:$PORT/healthz")" = "ok" ] || fail "healthz"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/status")
[ "$code" = "401" ] || fail "the API answered $code without a token"
[ "$(api /api/status | field sets)" = "$nsets" ] || fail "want the example's $nsets sets"
[ "$(api /api/version | field version)" = "v0.0.0-smoke" ] || fail "version"
[ "$(api /api/version | field config_rev)" = "v1" ] || fail "config_rev"
echo "ok: healthz, token required, $nsets sets, version and config commit"

say "the database survives the pod"
kubectl -n "$NS" scale deployment/popcorn --replicas=0 >/dev/null
kubectl -n "$NS" wait --for=delete pod -l app.kubernetes.io/name=popcorn --timeout=60s >/dev/null 2>&1 || true
peek() { # command to run against the volume
	kubectl -n "$NS" delete pod peek --ignore-not-found >/dev/null
	kubectl -n "$NS" run peek --image=busybox:1.36 --restart=Never --overrides='{
	  "spec": {"securityContext": {"runAsUser": 65532, "runAsGroup": 65532},
	    "containers": [{"name": "peek", "image": "busybox:1.36", "command": ["sh", "-c", "'"$1"'"],
	      "volumeMounts": [{"name": "data", "mountPath": "/data"}]}],
	    "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "popcorn-data"}}]}}' >/dev/null
	kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded pod/peek --timeout=90s >/dev/null || {
		kubectl -n "$NS" logs peek || true
		fail "peek: $1"
	}
	kubectl -n "$NS" logs peek
}
peek 'test -s /data/popcorn.db && touch /data/marker && echo db present'
kubectl -n "$NS" scale deployment/popcorn --replicas=1 >/dev/null
kubectl -n "$NS" rollout status deployment/popcorn --timeout=120s >/dev/null
forward
[ "$(api /api/status | field sets)" = "$nsets" ] || fail "after restart"
kubectl -n "$NS" scale deployment/popcorn --replicas=0 >/dev/null
kubectl -n "$NS" wait --for=delete pod -l app.kubernetes.io/name=popcorn --timeout=60s >/dev/null 2>&1 || true
peek 'test -f /data/marker && test -s /data/popcorn.db && echo marker and db kept'
kubectl -n "$NS" delete pod peek --ignore-not-found >/dev/null
kubectl -n "$NS" scale deployment/popcorn --replicas=1 >/dev/null
kubectl -n "$NS" rollout status deployment/popcorn --timeout=120s >/dev/null

say "upgrade to a new config"
cp -R examples/popcorn-config "$work/v2"
rm "$work/v2/prompt-sets/head-to-heads.toml"
config_image v2 "$work/v2"
install v2
forward
[ "$(api /api/status | field sets)" = "$((nsets - 1))" ] || fail "want $((nsets - 1)) sets after the upgrade"
[ "$(api /api/version | field config_rev)" = "v2" ] || fail "config_rev after the upgrade"
echo "ok: upgraded in place, new config live"

say "a broken config fails the deploy, and Helm rolls it back"
# The chart replaces the pod rather than rolling (one instance per database), so a broken image
# is down until the rollback: popcorn check in the config repo's CI is the first line of defence.
cp -R examples/popcorn-config "$work/broken"
printf 'id = "broken"\nprompts = ["from before ids"]\n' >"$work/broken/prompt-sets/broken.toml"
config_image broken "$work/broken"
if helm upgrade popcorn charts/popcorn -n "$NS" --rollback-on-failure --timeout 60s \
	--set image.repository=popcorn-config --set image.tag=broken --set image.pullPolicy=Never \
	--set existingSecret=popcorn >"$work/broken.log" 2>&1; then
	fail "a broken config deployed"
fi
grep -qi "roll" "$work/broken.log" || { cat "$work/broken.log"; fail "no rollback"; }
kubectl -n "$NS" rollout status deployment/popcorn --timeout=120s >/dev/null
forward
[ "$(api /api/version | field config_rev)" = "v2" ] || fail "not back on v2"
echo "ok: the deploy failed and rolled itself back to v2"

say "all good"
