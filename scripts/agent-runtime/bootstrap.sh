#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/agent-runtime/bootstrap.sh [--check | --apply]

  --check  Validate the repo-local Git safety configuration (default).
  --apply  Apply the approved, reversible repo-local Git configuration.
EOF
}

mode="check"
case "${1:-}" in
  "" | --check)
    ;;
  --apply)
    mode="apply"
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

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "ERROR: not inside a Git worktree" >&2
  exit 1
}
cd "$repo_root"

git remote get-url origin >/dev/null 2>&1 || {
  echo "ERROR: missing origin remote" >&2
  exit 1
}
git remote get-url upstream >/dev/null 2>&1 || {
  echo "ERROR: missing upstream remote" >&2
  exit 1
}

matches_github_repo() {
  local remote_url=$1
  local repository=$2
  case "$remote_url" in
    "https://github.com/$repository" | \
      "https://github.com/$repository.git" | \
      "git@github.com:$repository" | \
      "git@github.com:$repository.git" | \
      "ssh://git@github.com/$repository" | \
      "ssh://git@github.com/$repository.git")
      return 0
      ;;
  esac
  return 1
}

validate_remote_urls() {
  local remote_name=$1
  local repository=$2
  local config_key=$3
  local remote_url
  local found=false
  while IFS= read -r remote_url; do
    [[ -n "$remote_url" ]] || continue
    found=true
    if ! matches_github_repo "$remote_url" "$repository"; then
      echo "ERROR: unexpected $remote_name URL in $config_key: $remote_url" >&2
      return 1
    fi
  done < <(git config --local --get-all "$config_key" 2>/dev/null || true)
  if ! "$found"; then
    echo "ERROR: missing $remote_name URL in $config_key" >&2
    return 1
  fi
}

validate_remote_urls origin bowenQT/new-api remote.origin.url
validate_remote_urls upstream QuantumNous/new-api remote.upstream.url

if git config --local --get-all remote.origin.pushurl >/dev/null 2>&1; then
  validate_remote_urls origin bowenQT/new-api remote.origin.pushurl
else
  validate_remote_urls origin bowenQT/new-api remote.origin.url
fi

validate_effective_urls() {
  local remote_name=$1
  local repository=$2
  local url_mode=$3
  local effective_url
  local found=false
  local git_args=(remote get-url --all "$remote_name")
  if [[ "$url_mode" == "push" ]]; then
    git_args=(remote get-url --push --all "$remote_name")
  fi
  while IFS= read -r effective_url; do
    [[ -n "$effective_url" ]] || continue
    found=true
    if ! matches_github_repo "$effective_url" "$repository"; then
      echo "ERROR: unexpected effective $remote_name $url_mode URL: $effective_url" >&2
      return 1
    fi
  done < <(git "${git_args[@]}")
  if ! "$found"; then
    echo "ERROR: missing effective $remote_name $url_mode URL" >&2
    return 1
  fi
}

validate_effective_urls origin bowenQT/new-api fetch
validate_effective_urls origin bowenQT/new-api push
validate_effective_urls upstream QuantumNous/new-api fetch

origin_url=$(git remote get-url origin)
upstream_url=$(git remote get-url upstream)

git_version=$(git version | awk '{print $3}')
git_major=${git_version%%.*}
git_version_rest=${git_version#*.}
git_minor=${git_version_rest%%.*}
if (( git_major < 2 || (git_major == 2 && git_minor < 38) )); then
  echo "ERROR: Git 2.38+ is required; found $git_version" >&2
  exit 1
fi

operation_paths=(MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD BISECT_LOG rebase-merge rebase-apply)
for operation_path in "${operation_paths[@]}"; do
  resolved_path=$(git rev-parse --git-path "$operation_path")
  if [[ -e "$resolved_path" ]]; then
    echo "ERROR: Git operation in progress: $operation_path" >&2
    exit 1
  fi
done

if [[ "$mode" == "apply" ]]; then
  git config --local remote.upstream.pushurl DISABLED
  git config --local remote.pushDefault origin
  git config --local push.default simple
  git config --local pull.ff only
  git config --local fetch.prune false
  git config --local rebase.autoStash false
  git config --local merge.autoStash false
  git config --local merge.conflictStyle zdiff3
  git config --local rerere.enabled true
  git config --local rerere.autoupdate false

  if git show-ref --verify --quiet refs/heads/downstream/main &&
    git show-ref --verify --quiet refs/remotes/origin/downstream/main; then
    git config --local branch.downstream/main.remote origin
    git config --local branch.downstream/main.merge refs/heads/downstream/main
  fi
fi

check_config() {
  local key=$1
  local expected=$2
  local actual
  actual=$(git config --local --get "$key" 2>/dev/null || true)
  if [[ "$actual" != "$expected" ]]; then
    echo "ERROR: $key expected '$expected', found '${actual:-<unset>}'" >&2
    return 1
  fi
  printf '%s=%s\n' "$key" "$actual"
}

check_config remote.upstream.pushurl DISABLED
check_config remote.pushDefault origin
check_config push.default simple
check_config pull.ff only
check_config fetch.prune false
check_config rebase.autoStash false
check_config merge.autoStash false
check_config merge.conflictStyle zdiff3
check_config rerere.enabled true
check_config rerere.autoupdate false

effective_upstream_push=$(git remote get-url --push --all upstream 2>/dev/null || true)
if [[ "$effective_upstream_push" != "DISABLED" ]]; then
  echo "ERROR: effective upstream push URL must be DISABLED, found '${effective_upstream_push:-<unset>}'" >&2
  exit 1
fi

main_remote=$(git config --local --get branch.main.remote 2>/dev/null || true)
if [[ -n "$main_remote" && "$main_remote" != "origin" ]]; then
  echo "ERROR: branch.main.remote must be origin, found $main_remote" >&2
  exit 1
fi

if git show-ref --verify --quiet refs/heads/downstream/main &&
  git show-ref --verify --quiet refs/remotes/origin/downstream/main; then
  check_config branch.downstream/main.remote origin
  check_config branch.downstream/main.merge refs/heads/downstream/main
fi

printf 'repository=%s\n' "$repo_root"
printf 'origin=%s\n' "$origin_url"
printf 'upstream_fetch=%s\n' "$upstream_url"
printf 'git_version=%s\n' "$git_version"
printf 'bootstrap=READY\n'
