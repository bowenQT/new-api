#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/agent-runtime/preflight.sh [--mode <mode>] [--fetch]
       [--allow-sensitive-path <repo-relative-path>]

Reports task authority and Git/worktree state. --fetch refreshes origin and
upstream remote-tracking refs without checking out or merging branches.

Modes:
  read-only  Inspect without preparing to edit (default).
  edit       Start downstream implementation from downstream/main.
  upstream-edit  Start an upstream contribution without downstream-only history.
  sync       Audit/synchronize upstream; requires a clean worktree.
  bootstrap  Initialize the runtime on an approved branch.
EOF
}

fetch_remotes=false
mode=read-only
allowed_sensitive_paths=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --fetch)
      fetch_remotes=true
      shift
      ;;
    --mode)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      mode=$2
      shift 2
      ;;
    --allow-sensitive-path)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      allowed_sensitive_paths+=("$2")
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

case "$mode" in
  read-only | edit | upstream-edit | sync | bootstrap)
    ;;
  *)
    echo "ERROR: unknown preflight mode: $mode" >&2
    usage >&2
    exit 64
    ;;
esac

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "ERROR: not inside a Git worktree" >&2
  exit 1
}
cd "$repo_root"

scripts/agent-runtime/bootstrap.sh --check >/dev/null

operation_paths=(MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD BISECT_LOG rebase-merge rebase-apply)
for operation_path in "${operation_paths[@]}"; do
  resolved_path=$(git rev-parse --git-path "$operation_path")
  if [[ -e "$resolved_path" ]]; then
    echo "ERROR: Git operation in progress: $operation_path" >&2
    exit 1
  fi
done

if "$fetch_remotes"; then
  git fetch --no-tags origin
  git fetch --no-tags upstream
fi

branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null || printf 'DETACHED')
head_sha=$(git rev-parse HEAD)
status=$(git status --porcelain=v1 --untracked-files=all)

if [[ "$mode" == "edit" || "$mode" == "upstream-edit" ]]; then
  if [[ "$branch" == "DETACHED" || "$branch" == "main" || "$branch" == "downstream/main" || "$branch" == "deploy/sgp1" ]]; then
    echo "ERROR: $mode mode requires a symbolic topic branch; current branch is $branch" >&2
    exit 1
  fi
fi

if [[ "$mode" == "edit" ]]; then
  downstream_base_ref=refs/remotes/origin/downstream/main
  git rev-parse --verify --quiet "$downstream_base_ref^{commit}" >/dev/null || {
    echo "ERROR: edit mode requires origin/downstream/main; fetch or bootstrap the fork first" >&2
    exit 1
  }
  if ! git merge-base --is-ancestor "$downstream_base_ref" "$head_sha"; then
    echo "ERROR: edit branch $branch does not descend from origin/downstream/main" >&2
    exit 1
  fi
fi

if [[ "$mode" == "upstream-edit" ]]; then
  upstream_base_ref=refs/remotes/upstream/main
  git rev-parse --verify --quiet "$upstream_base_ref^{commit}" >/dev/null || {
    echo "ERROR: upstream-edit mode requires upstream/main; fetch or bootstrap the fork first" >&2
    exit 1
  }
  if ! git merge-base --is-ancestor "$upstream_base_ref" "$head_sha"; then
    echo "ERROR: upstream contribution branch $branch does not descend from upstream/main" >&2
    exit 1
  fi

  downstream_base_ref=refs/remotes/origin/downstream/main
  git rev-parse --verify --quiet "$downstream_base_ref^{commit}" >/dev/null || {
    echo "ERROR: upstream-edit mode requires origin/downstream/main; fetch or bootstrap the fork first" >&2
    exit 1
  }
  downstream_common_bases=$(git merge-base --all "$downstream_base_ref" "$head_sha") || {
    echo "ERROR: cannot compare upstream contribution with origin/downstream/main" >&2
    exit 1
  }
  [[ -n "$downstream_common_bases" ]] || {
    echo "ERROR: upstream contribution has no common base with origin/downstream/main" >&2
    exit 1
  }
  while IFS= read -r downstream_common_base; do
    if ! git merge-base --is-ancestor "$downstream_common_base" "$upstream_base_ref"; then
      echo "ERROR: upstream contribution branch $branch contains downstream-only history" >&2
      exit 1
    fi
  done <<< "$downstream_common_bases"
fi
if [[ "$mode" == "sync" && -n "$status" ]]; then
  echo "ERROR: sync mode requires a clean worktree" >&2
  exit 1
fi
if [[ "$mode" != "bootstrap" && "$branch" == "main" && -n "$status" ]]; then
  echo "ERROR: origin/main is a protected mirror; downstream changes do not belong on main" >&2
  exit 1
fi
if [[ "$mode" != "bootstrap" && "$branch" == "downstream/main" && -n "$status" ]]; then
  echo "ERROR: downstream/main is a stable integration branch; use a feature or maintenance branch" >&2
  exit 1
fi

if [[ -n "$status" ]]; then
  while IFS= read -r status_line; do
    path=${status_line:3}
    case "$path" in
      *.pem | *.key | *.p12 | *.pfx | *.db | *.sqlite | *.sqlite3 | *.dump | *.sql | *.sql.gz | */.env | .env)
        echo "ERROR: suspicious uncommitted credential/data artifact: $path" >&2
        exit 1
        ;;
    esac
  done <<< "$status"
fi

is_allowed_sensitive_path() {
  local candidate=$1
  local allowed_path
  for allowed_path in "${allowed_sensitive_paths[@]}"; do
    if [[ "$candidate" == "$allowed_path" ]]; then
      return 0
    fi
  done
  return 1
}

while IFS= read -r -d '' sensitive_path; do
  if ! is_allowed_sensitive_path "$sensitive_path"; then
    echo "ERROR: ignored credential/data artifact requires explicit path acknowledgement: $sensitive_path" >&2
    exit 1
  fi
done < <(
  git ls-files --others --ignored --exclude-standard -z -- \
    '.env' '**/.env' \
    '*.pem' '**/*.pem' '*.key' '**/*.key' '*.p12' '**/*.p12' '*.pfx' '**/*.pfx' \
    '*.db' '**/*.db' '*.sqlite' '**/*.sqlite' '*.sqlite3' '**/*.sqlite3' \
    '*.dump' '**/*.dump' '*.sql' '**/*.sql' '*.sql.gz' '**/*.sql.gz'
)

printf 'repository=%s\n' "$repo_root"
printf 'mode=%s\n' "$mode"
printf 'branch=%s\n' "$branch"
printf 'head=%s\n' "$head_sha"

for ref_name in upstream/main origin/main origin/downstream/main; do
  if git rev-parse --verify --quiet "$ref_name^{commit}" >/dev/null; then
    printf '%s=%s\n' "${ref_name//\//_}" "$(git rev-parse "$ref_name^{commit}")"
  else
    printf '%s=%s\n' "${ref_name//\//_}" '<missing>'
  fi
done

if [[ -n "$status" ]]; then
  printf 'worktree=DIRTY\n'
  printf '%s\n' "$status"
else
  printf 'worktree=CLEAN\n'
fi

printf 'worktrees:\n'
git worktree list --porcelain
printf 'preflight=READY\n'
