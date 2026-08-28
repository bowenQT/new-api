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

worktree_paths=()
while IFS= read -r -d '' worktree_record; do
  case "$worktree_record" in
    'worktree '*)
      worktree_paths+=("${worktree_record#worktree }")
      ;;
  esac
done < <(git worktree list --porcelain -z)
if (( ${#worktree_paths[@]} == 0 )); then
  echo "ERROR: repository has no registered worktrees" >&2
  exit 1
fi
for worktree_path in "${worktree_paths[@]}"; do
  if [[ ! -d "$worktree_path" ]]; then
    echo "ERROR: registered worktree is unavailable: $worktree_path" >&2
    exit 1
  fi
done

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

protected_config_regex='^(remote\.(origin|upstream)\.(url|pushurl)|remote\.origin\.(fetch|push)|remote\.pushdefault|url\..*\.(insteadof|pushinsteadof)|push\.default|pull\.ff|fetch\.prune|rebase\.autostash|merge\.autostash|merge\.conflictstyle|rerere\.enabled|rerere\.autoupdate|branch\..*\.pushremote|branch\.main\.(remote|merge)|branch\.downstream/main\.(remote|merge))$'

resolve_include_path() {
  local worktree_path=$1
  local origin=$2
  local include_path=$3
  local origin_path
  local origin_dir
  case "$origin" in
    file:*)
      origin_path=${origin#file:}
      ;;
    *)
      echo "ERROR: cannot validate branch-conditioned include from $origin" >&2
      return 1
      ;;
  esac
  case "$origin_path" in
    /*)
      ;;
    *)
      origin_path="$worktree_path/$origin_path"
      ;;
  esac
  origin_dir=$(cd "$(dirname "$origin_path")" 2>/dev/null && pwd -P) || {
    echo "ERROR: cannot resolve branch-conditioned include origin: $origin_path" >&2
    return 1
  }
  case "$include_path" in
    /*)
      printf '%s\n' "$include_path"
      ;;
    '~/'*)
      printf '%s/%s\n' "${HOME}" "${include_path:2}"
      ;;
    '%(home)/'*)
      printf '%s/%s\n' "${HOME}" "${include_path#%(home)/}"
      ;;
    '%('* | '~'*)
      echo "ERROR: cannot safely resolve branch-conditioned include path: $include_path" >&2
      return 1
      ;;
    *)
      printf '%s/%s\n' "$origin_dir" "$include_path"
      ;;
  esac
}

validate_conditioned_config_file() {
  local worktree_path=$1
  local config_path=$2
  local baseline_values
  local protected_values
  if [[ ! -f "$config_path" ]]; then
    echo "ERROR: branch-conditioned include is unavailable: $config_path" >&2
    return 1
  fi
  baseline_values=$(git -C "$worktree_path" config --includes --get-regexp "$protected_config_regex" 2>/dev/null || true)
  protected_values=$(git -C "$worktree_path" -c include.path="$config_path" \
    config --includes --get-regexp "$protected_config_regex" 2>/dev/null || true)
  if [[ "$protected_values" != "$baseline_values" ]]; then
    echo "ERROR: branch-conditioned safety override in $config_path: $protected_values" >&2
    return 1
  fi
}

validate_branch_conditioned_config() {
  local worktree_path=$1
  local origin
  local config_entry
  local include_path
  local config_path
  while IFS= read -r -d '' origin && IFS= read -r -d '' config_entry; do
    include_path=${config_entry#*$'\n'}
    config_path=$(resolve_include_path "$worktree_path" "$origin" "$include_path") || return 1
    validate_conditioned_config_file "$worktree_path" "$config_path" || return 1
  done < <(git -C "$worktree_path" config --null --show-origin --get-regexp '^includeif\.onbranch:.*\.path$' 2>/dev/null || true)
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
  local worktree_path=$1
  local remote_name=$2
  local repository=$3
  local url_mode=$4
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
      echo "ERROR: unexpected effective $remote_name $url_mode URL in worktree $worktree_path: $effective_url" >&2
      return 1
    fi
  done < <(git -C "$worktree_path" "${git_args[@]}")
  if ! "$found"; then
    echo "ERROR: missing effective $remote_name $url_mode URL in worktree $worktree_path" >&2
    return 1
  fi
}

validate_effective_upstream_push() {
  local worktree_path=$1
  local actual
  local count
  actual=$(git -C "$worktree_path" remote get-url --push --all upstream 2>/dev/null || true)
  count=$({ git -C "$worktree_path" remote get-url --push --all upstream 2>/dev/null || true; } | awk 'END { print NR + 0 }')
  if [[ "$count" != "1" || "$actual" != "DISABLED" ]]; then
    echo "ERROR: effective upstream push URL must be exactly DISABLED in worktree $worktree_path, found $count: '${actual:-<unset>}'" >&2
    return 1
  fi
}

validate_effective_unset() {
  local worktree_path=$1
  local key=$2
  local actual
  local count
  actual=$(git -C "$worktree_path" config --get-all "$key" 2>/dev/null || true)
  count=$({ git -C "$worktree_path" config --get-all "$key" 2>/dev/null || true; } | awk 'END { print NR + 0 }')
  if [[ "$count" != "0" ]]; then
    echo "ERROR: worktree $worktree_path effective $key must be unset, found $count: '$actual'" >&2
    return 1
  fi
}

validate_effective_branch_push_remotes() {
  local worktree_path=$1
  local actual
  actual=$(git -C "$worktree_path" config --get-regexp '^branch\..*\.pushremote$' 2>/dev/null || true)
  if [[ -n "$actual" ]]; then
    echo "ERROR: worktree $worktree_path effective branch pushRemote must be unset: $actual" >&2
    return 1
  fi
}

for worktree_path in "${worktree_paths[@]}"; do
  validate_branch_conditioned_config "$worktree_path"
  validate_effective_urls "$worktree_path" origin bowenQT/new-api fetch
  validate_effective_urls "$worktree_path" origin bowenQT/new-api push
  validate_effective_urls "$worktree_path" upstream QuantumNous/new-api fetch
done

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
  git config --local --unset-all remote.origin.push 2>/dev/null || true
  while IFS= read -r branch_push_key; do
    [[ -n "$branch_push_key" ]] || continue
    git config --local --unset-all "$branch_push_key"
  done < <(git config --local --name-only --get-regexp '^branch\..*\.pushremote$' 2>/dev/null || true)
  git config --local --replace-all remote.origin.fetch \
    '+refs/heads/*:refs/remotes/origin/*'
  git config --local --replace-all remote.upstream.pushurl DISABLED
  git config --local --replace-all remote.pushDefault origin
  git config --local --replace-all push.default simple
  git config --local --replace-all pull.ff only
  git config --local --replace-all fetch.prune false
  git config --local --replace-all rebase.autoStash false
  git config --local --replace-all merge.autoStash false
  git config --local --replace-all merge.conflictStyle zdiff3
  git config --local --replace-all rerere.enabled true
  git config --local --replace-all rerere.autoupdate false

  if git show-ref --verify --quiet refs/heads/main; then
    git config --local --replace-all branch.main.remote origin
    git config --local --replace-all branch.main.merge refs/heads/main
  fi
  if git show-ref --verify --quiet refs/heads/downstream/main &&
    git show-ref --verify --quiet refs/remotes/origin/downstream/main; then
    git config --local --replace-all branch.downstream/main.remote origin
    git config --local --replace-all branch.downstream/main.merge refs/heads/downstream/main
  fi
fi

for worktree_path in "${worktree_paths[@]}"; do
  validate_effective_upstream_push "$worktree_path"
  validate_effective_unset "$worktree_path" remote.origin.push
  validate_effective_branch_push_remotes "$worktree_path"
done

check_local_config() {
  local key=$1
  local expected=$2
  local actual
  local count
  actual=$(git config --local --get-all "$key" 2>/dev/null || true)
  count=$({ git config --local --get-all "$key" 2>/dev/null || true; } | awk 'END { print NR + 0 }')
  if [[ "$count" != "1" || "$actual" != "$expected" ]]; then
    echo "ERROR: $key expected exactly one value '$expected', found $count: '${actual:-<unset>}'" >&2
    return 1
  fi
  printf '%s=%s\n' "$key" "$actual"
}

check_effective_multi_config() {
  local worktree_path=$1
  local key=$2
  local expected=$3
  local actual
  local count
  actual=$(git -C "$worktree_path" config --get-all "$key" 2>/dev/null || true)
  count=$({ git -C "$worktree_path" config --get-all "$key" 2>/dev/null || true; } | awk 'END { print NR + 0 }')
  if [[ "$count" != "1" || "$actual" != "$expected" ]]; then
    echo "ERROR: worktree $worktree_path effective $key expected exactly one value '$expected', found $count: '${actual:-<unset>}'" >&2
    return 1
  fi
  printf 'worktree.%s.effective.%s=%s\n' "$worktree_path" "$key" "$actual"
}

check_effective_scalar_config() {
  local worktree_path=$1
  local key=$2
  local expected=$3
  local actual
  actual=$(git -C "$worktree_path" config --get "$key" 2>/dev/null || true)
  if [[ "$actual" != "$expected" ]]; then
    echo "ERROR: worktree $worktree_path effective $key expected '$expected', found '${actual:-<unset>}'" >&2
    return 1
  fi
  printf 'worktree.%s.effective.%s=%s\n' "$worktree_path" "$key" "$actual"
}

required_multi_configs=(
  'remote.origin.fetch +refs/heads/*:refs/remotes/origin/*'
  'remote.upstream.pushurl DISABLED'
)
required_scalar_configs=(
  'remote.pushDefault origin'
  'push.default simple'
  'pull.ff only'
  'fetch.prune false'
  'rebase.autoStash false'
  'merge.autoStash false'
  'merge.conflictStyle zdiff3'
  'rerere.enabled true'
  'rerere.autoupdate false'
)
for required_config in "${required_multi_configs[@]}"; do
  read -r key expected <<< "$required_config"
  check_local_config "$key" "$expected"
  for worktree_path in "${worktree_paths[@]}"; do
    check_effective_multi_config "$worktree_path" "$key" "$expected"
  done
done
for required_config in "${required_scalar_configs[@]}"; do
  read -r key expected <<< "$required_config"
  check_local_config "$key" "$expected"
  for worktree_path in "${worktree_paths[@]}"; do
    check_effective_scalar_config "$worktree_path" "$key" "$expected"
  done
done

if git show-ref --verify --quiet refs/heads/main; then
  check_local_config branch.main.remote origin
  check_local_config branch.main.merge refs/heads/main
  for worktree_path in "${worktree_paths[@]}"; do
    check_effective_scalar_config "$worktree_path" branch.main.remote origin
    check_effective_multi_config "$worktree_path" branch.main.merge refs/heads/main
  done
fi

if git show-ref --verify --quiet refs/heads/downstream/main &&
  git show-ref --verify --quiet refs/remotes/origin/downstream/main; then
  check_local_config branch.downstream/main.remote origin
  check_local_config branch.downstream/main.merge refs/heads/downstream/main
  for worktree_path in "${worktree_paths[@]}"; do
    check_effective_scalar_config "$worktree_path" branch.downstream/main.remote origin
    check_effective_multi_config "$worktree_path" branch.downstream/main.merge refs/heads/downstream/main
  done
fi

printf 'repository=%s\n' "$repo_root"
printf 'origin=%s\n' "$origin_url"
printf 'upstream_fetch=%s\n' "$upstream_url"
printf 'git_version=%s\n' "$git_version"
printf 'bootstrap=READY\n'
