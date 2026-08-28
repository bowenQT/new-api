#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/agent-runtime/upstream-audit.sh [--fetch | --no-fetch] [--target <ref>]

Read-only audit of the upstream mirror and downstream integration branch.
The merge simulation writes temporary Git objects outside the repository.
EOF
}

fetch_remotes=true
target_ref=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --fetch)
      fetch_remotes=true
      shift
      ;;
    --no-fetch)
      fetch_remotes=false
      shift
      ;;
    --target)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      target_ref=$2
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

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "ERROR: not inside a Git worktree" >&2
  exit 1
}
cd "$repo_root"
scripts/agent-runtime/preflight.sh --mode sync >/dev/null

if "$fetch_remotes"; then
  git fetch --no-tags origin
  git fetch --no-tags upstream
fi

upstream_ref=refs/remotes/upstream/main
mirror_ref=refs/remotes/origin/main
for required_ref in "$upstream_ref" "$mirror_ref"; do
  git rev-parse --verify --quiet "$required_ref^{commit}" >/dev/null || {
    echo "ERROR: missing required ref: $required_ref" >&2
    exit 1
  }
done

if [[ -z "$target_ref" ]]; then
  if git rev-parse --verify --quiet refs/remotes/origin/downstream/main^{commit} >/dev/null; then
    target_ref=refs/remotes/origin/downstream/main
  elif git rev-parse --verify --quiet refs/heads/downstream/main^{commit} >/dev/null; then
    target_ref=refs/heads/downstream/main
  else
    echo "ERROR: downstream/main does not exist locally or on origin" >&2
    exit 1
  fi
fi
git rev-parse --verify --quiet "$target_ref^{commit}" >/dev/null || {
  echo "ERROR: target is not a commit: $target_ref" >&2
  exit 1
}

upstream_sha=$(git rev-parse "$upstream_ref^{commit}")
mirror_sha=$(git rev-parse "$mirror_ref^{commit}")
target_sha=$(git rev-parse "$target_ref^{commit}")
read -r mirror_only upstream_only <<< "$(git rev-list --left-right --count "$mirror_sha...$upstream_sha")"

mirror_state=DIVERGED
audit_exit=0
if [[ "$mirror_only" == "0" && "$upstream_only" == "0" ]]; then
  mirror_state=IN_SYNC
elif git merge-base --is-ancestor "$mirror_sha" "$upstream_sha"; then
  mirror_state=UPSTREAM_AHEAD_FF
  audit_exit=2
elif git merge-base --is-ancestor "$upstream_sha" "$mirror_sha"; then
  mirror_state=FORK_AHEAD_ONLY
  audit_exit=2
else
  audit_exit=2
fi

merge_base=$(git merge-base "$upstream_sha" "$target_sha")
read -r target_behind target_ahead <<< "$(git rev-list --left-right --count "$upstream_sha...$target_sha")"

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/newapi-upstream-audit.XXXXXX")
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

local_paths="$tmp_dir/local-paths"
upstream_paths="$tmp_dir/upstream-paths"
overlap_paths="$tmp_dir/overlap-paths"
risk_paths="$tmp_dir/risk-paths"
merge_output="$tmp_dir/merge-output"
git diff --no-renames --name-only "$merge_base..$target_sha" | LC_ALL=C sort -u > "$local_paths"
git diff --no-renames --name-only "$merge_base..$upstream_sha" | LC_ALL=C sort -u > "$upstream_paths"
comm -12 "$local_paths" "$upstream_paths" > "$overlap_paths"

cat "$local_paths" "$upstream_paths" | LC_ALL=C sort -u |
  grep -E '^(router/|controller/|service/|model/|relay/|relaykit/|middleware/|setting/|common/|dto/|types/|constant/|pkg/|oauth/|i18n/|web/src/|Dockerfile$|docker-compose[^/]*\.yml$|deploy/|\.github/|AGENTS\.md$)' \
    > "$risk_paths" || true

common_git_dir=$(git rev-parse --git-common-dir)
common_git_dir=$(cd "$common_git_dir" && pwd)
temporary_objects="$tmp_dir/objects"
mkdir -p "$temporary_objects"

merge_state=NO_TEXT_CONFLICT
set +e
GIT_OBJECT_DIRECTORY="$temporary_objects" \
  GIT_ALTERNATE_OBJECT_DIRECTORIES="$common_git_dir/objects" \
  git merge-tree --write-tree --name-only --messages "$target_sha" "$upstream_sha" \
    > "$merge_output" 2>&1
merge_exit=$?
set -e
case "$merge_exit" in
  0)
    ;;
  1)
    merge_state=TEXT_CONFLICT
    audit_exit=3
    ;;
  *)
    printf 'merge_messages:\n' >&2
    cat "$merge_output" >&2
    echo "ERROR: merge simulation failed to execute (exit $merge_exit)" >&2
    exit 4
    ;;
esac

printf 'upstream_sha=%s\n' "$upstream_sha"
printf 'origin_main_sha=%s\n' "$mirror_sha"
printf 'target_ref=%s\n' "$target_ref"
printf 'target_sha=%s\n' "$target_sha"
printf 'merge_base=%s\n' "$merge_base"
printf 'mirror_state=%s\n' "$mirror_state"
printf 'origin_only_commits=%s\n' "$mirror_only"
printf 'upstream_only_commits=%s\n' "$upstream_only"
printf 'target_behind_upstream=%s\n' "$target_behind"
printf 'target_ahead_upstream=%s\n' "$target_ahead"
printf 'merge_simulation=%s\n' "$merge_state"

printf 'local_delta_paths:\n'
if [[ -s "$local_paths" ]]; then cat "$local_paths"; else printf '(none)\n'; fi
printf 'upstream_delta_paths:\n'
if [[ -s "$upstream_paths" ]]; then cat "$upstream_paths"; else printf '(none)\n'; fi
printf 'overlapping_paths:\n'
if [[ -s "$overlap_paths" ]]; then cat "$overlap_paths"; else printf '(none)\n'; fi
printf 'semantic_review_paths:\n'
if [[ -s "$risk_paths" ]]; then cat "$risk_paths"; else printf '(none)\n'; fi

if [[ "$merge_state" == "TEXT_CONFLICT" ]]; then
  printf 'merge_messages:\n'
  cat "$merge_output"
fi

if [[ "$mirror_state" != "IN_SYNC" ]]; then
  echo "ERROR: origin/main is not an exact mirror of upstream/main" >&2
fi
if [[ "$merge_state" == "TEXT_CONFLICT" ]]; then
  echo "ERROR: merge simulation found text conflicts; semantic review is also required" >&2
fi

exit "$audit_exit"
