#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/agent-runtime/verify.sh <profile> [--allow-dirty] [--expected-head <sha>]

Profiles: docs, backend, relaykit, frontend, full, database, deployment

Default mode requires a clean worktree and produces exact-HEAD evidence.
--allow-dirty is iteration-only and labels evidence as WORKTREE_SNAPSHOT.
EOF
}

[[ $# -ge 1 ]] || {
  usage >&2
  exit 64
}
profile=$1
shift

allow_dirty=false
expected_head=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --allow-dirty)
      allow_dirty=true
      shift
      ;;
    --expected-head)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      expected_head=$2
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 64
      ;;
  esac
done

case "$profile" in
  docs | backend | relaykit | frontend | full | database | deployment)
    ;;
  *)
    echo "ERROR: unknown profile: $profile" >&2
    usage >&2
    exit 64
    ;;
esac

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "ERROR: not inside a Git worktree" >&2
  exit 1
}
cd "$repo_root"
source scripts/agent-runtime/lib.sh

head_start=$(git rev-parse HEAD)
if [[ -n "$expected_head" ]]; then
  expected_head=$(git rev-parse --verify "$expected_head^{commit}" 2>/dev/null) || {
    echo "ERROR: expected HEAD is not a commit" >&2
    exit 1
  }
  if [[ "$head_start" != "$expected_head" ]]; then
    echo "ERROR: HEAD $head_start does not match expected $expected_head" >&2
    exit 1
  fi
fi

status_start=$(git status --porcelain=v1 --untracked-files=all)
evidence_scope=EXACT_HEAD
if [[ -n "$status_start" ]]; then
  if ! "$allow_dirty"; then
    echo "ERROR: exact-HEAD verification requires a clean worktree" >&2
    printf '%s\n' "$status_start" >&2
    exit 1
  fi
  evidence_scope=WORKTREE_SNAPSHOT
fi

workspace_start=$(runtime_workspace_fingerprint)
dependencies_start=$(runtime_dependency_fingerprint)
started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
runtime_tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/newapi-verify.XXXXXX")
dist_isolated=false
dist_had_original=false
receipt_tmp=""

cleanup() {
  if "$dist_isolated"; then
    rm -rf web/dist
    if "$dist_had_original"; then
      mv "$runtime_tmp_dir/original-dist" web/dist
    fi
  fi
  if [[ -n "$receipt_tmp" && -e "$receipt_tmp" ]]; then
    rm -f "$receipt_tmp"
  fi
  rm -rf "$runtime_tmp_dir"
}
trap cleanup EXIT

prepare_isolated_dist() {
  if "$dist_isolated"; then
    return
  fi
  if [[ -e web/dist || -L web/dist ]]; then
    mv web/dist "$runtime_tmp_dir/original-dist"
    dist_had_original=true
  fi
  dist_isolated=true
}

ensure_frontend_placeholder() {
  prepare_isolated_dist
  if [[ ! -f web/dist/index.html ]]; then
    mkdir -p web/dist
    touch web/dist/index.html
  fi
}

run_docs() {
  local required_file
  for required_file in \
    .agents/runtime/README.md \
    .agents/runtime/task-envelope.md \
    .agents/runtime/closeout.md \
    .agents/runtime/verification-matrix.md \
    .agents/skills/upstream-sync/SKILL.md \
    docs/downstream/README.md \
    docs/downstream/development.md \
    docs/downstream/fork-deltas.yaml \
    scripts/agent-runtime/bootstrap.sh \
    scripts/agent-runtime/closeout.sh \
    scripts/agent-runtime/lib.sh \
    scripts/agent-runtime/preflight.sh \
    scripts/agent-runtime/runtime_test.sh \
    scripts/agent-runtime/upstream-audit.sh \
    scripts/agent-runtime/validate_fork_deltas.go \
    scripts/agent-runtime/validate_fork_deltas_test.go \
    scripts/agent-runtime/verify.sh; do
    [[ -f "$required_file" ]] || {
      echo "ERROR: missing runtime file: $required_file" >&2
      return 1
    }
  done

  local runtime_script
  for runtime_script in scripts/agent-runtime/*.sh; do
    if [[ "$runtime_script" != "scripts/agent-runtime/lib.sh" ]]; then
      [[ -x "$runtime_script" ]] || {
        echo "ERROR: runtime script is not executable: $runtime_script" >&2
        return 1
      }
    fi
    bash -n "$runtime_script"
  done

  grep -Fq '### Downstream Fork Runtime' AGENTS.md
  grep -Fq 'origin/main` is an exact mirror' AGENTS.md
  grep -Fq 'name: upstream-sync' .agents/skills/upstream-sync/SKILL.md
  git diff --check
  git diff --cached --check
  unformatted_runtime_go=$(gofmt -l scripts/agent-runtime/*.go)
  if [[ -n "$unformatted_runtime_go" ]]; then
    echo "ERROR: gofmt required for runtime validator:" >&2
    printf '%s\n' "$unformatted_runtime_go" >&2
    return 1
  fi
  GOWORK=off go test ./scripts/agent-runtime
  scripts/agent-runtime/runtime_test.sh
  GOWORK=off go run ./scripts/agent-runtime --base upstream/main
  if git rev-parse --verify --quiet upstream/main^{commit} >/dev/null; then
    git diff --check upstream/main...HEAD
  fi
}

check_changed_go_format() {
  local changed_go_files="$runtime_tmp_dir/changed-go-files"
  local unformatted_go_files="$runtime_tmp_dir/unformatted-go-files"
  {
    if git rev-parse --verify --quiet upstream/main^{commit} >/dev/null; then
      git diff --name-only --diff-filter=ACMR upstream/main...HEAD -- '*.go'
    fi
    git diff --name-only --diff-filter=ACMR -- '*.go'
    git diff --cached --name-only --diff-filter=ACMR -- '*.go'
    git ls-files --others --exclude-standard -- '*.go'
  } | LC_ALL=C sort -u > "$changed_go_files"
  : > "$unformatted_go_files"
  while IFS= read -r go_file; do
    [[ -n "$go_file" && -f "$go_file" ]] || continue
    gofmt -l "$go_file" >> "$unformatted_go_files"
  done < "$changed_go_files"
  if [[ -s "$unformatted_go_files" ]]; then
    echo "ERROR: gofmt required for:" >&2
    cat "$unformatted_go_files" >&2
    return 1
  fi
}

run_relaykit() {
  local include_tests=${1:-true}
  (
    cd relaykit
    GOWORK=off go vet ./...
    GOWORK=off go build ./...
    if "$include_tests"; then
      GOWORK=off go test ./...
    fi
  )
}

run_backend() {
  check_changed_go_format
  ensure_frontend_placeholder
  GOWORK=off go vet ./...
  GOWORK=off go build ./...
  make test
}

run_frontend() {
  prepare_isolated_dist
  (
    cd web
    bun install --frozen-lockfile
    bun run lint
    bun run test
    bun run format:check
    bun run copyright:check
    bun run build:check
  )
}

run_database() {
  if [[ -z "${TEST_MYSQL_DSN:-}" || -z "${TEST_POSTGRES_DSN:-}" ]]; then
    echo "ERROR: database profile requires TEST_MYSQL_DSN and TEST_POSTGRES_DSN" >&2
    echo "ERROR: DSN values are never printed by this runtime" >&2
    return 1
  fi
  GOWORK=off go test ./model ./controller
}

run_deployment() {
  command -v docker >/dev/null || {
    echo "ERROR: docker is required for the deployment profile" >&2
    return 1
  }
  docker compose \
    --env-file deploy/sgp1/.env.example \
    -f deploy/sgp1/docker-compose.yml \
    config --quiet
}

case "$profile" in
  docs)
    run_docs
    ;;
  backend)
    run_backend
    ;;
  relaykit)
    run_relaykit
    ;;
  frontend)
    run_frontend
    ;;
  full)
    run_frontend
    run_backend
    run_relaykit false
    ;;
  database)
    run_database
    ;;
  deployment)
    run_deployment
    ;;
esac

head_end=$(git rev-parse HEAD)
workspace_end=$(runtime_workspace_fingerprint)
dependencies_end=$(runtime_dependency_fingerprint)
if [[ "$head_start" != "$head_end" ]]; then
  echo "ERROR: HEAD changed during verification: $head_start -> $head_end" >&2
  exit 1
fi
if [[ "$workspace_start" != "$workspace_end" ]]; then
  echo "ERROR: workspace changed during verification" >&2
  exit 1
fi
if [[ "$dependencies_start" != "$dependencies_end" ]]; then
  echo "ERROR: dependency/config fingerprint changed during verification" >&2
  exit 1
fi

finished_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
receipt_dir="$(git rev-parse --git-common-dir)/agent-runtime/receipts"
mkdir -p "$receipt_dir"
receipt_tmp=$(mktemp "$receipt_dir/.verification.XXXXXX")
receipt_unique=${receipt_tmp##*.verification.}
receipt_path="$receipt_dir/${finished_at//[:]/-}-${head_start:0:12}-$profile-$evidence_scope-$receipt_unique.receipt"
go_version=$(runtime_profile_go_version "$profile")
bun_version=$(runtime_profile_bun_version "$profile")
{
  printf 'schema_version\t1\n'
  printf 'result\tpassed\n'
  printf 'scope\t%s\n' "$evidence_scope"
  printf 'profile\t%s\n' "$profile"
  printf 'verified_sha\t%s\n' "$head_start"
  printf 'workspace_fingerprint\t%s\n' "$workspace_end"
  printf 'dependency_fingerprint\t%s\n' "$dependencies_end"
  printf 'started_at\t%s\n' "$started_at"
  printf 'finished_at\t%s\n' "$finished_at"
  printf 'git_version\t%s\n' "$(git version)"
  printf 'go_version\t%s\n' "$go_version"
  printf 'bun_version\t%s\n' "$bun_version"
} > "$receipt_tmp"
mv "$receipt_tmp" "$receipt_path"
receipt_tmp=""

printf 'verification=PASSED\n'
printf 'scope=%s\n' "$evidence_scope"
printf 'profile=%s\n' "$profile"
printf 'verified_sha=%s\n' "$head_start"
printf 'workspace_fingerprint=%s\n' "$workspace_end"
printf 'receipt=%s\n' "$receipt_path"
