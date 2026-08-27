#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/agent-runtime/closeout.sh --expected-head <sha> --profile <profile>
       [--receipt <path>] [--base <ref>] [--pr <number>]

Validates repository-delivery evidence only. It never merges, deploys, applies a
migration, modifies a PR, or removes a branch/worktree. Production and database
readbacks remain separate receipts in the closeout template.
EOF
}

expected_head=""
profile=""
receipt_path=""
base_ref=origin/downstream/main
pr_number=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --expected-head)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      expected_head=$2
      shift 2
      ;;
    --profile)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      profile=$2
      shift 2
      ;;
    --receipt)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      receipt_path=$2
      shift 2
      ;;
    --base)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      base_ref=$2
      shift 2
      ;;
    --pr)
      [[ $# -ge 2 ]] || {
        usage >&2
        exit 64
      }
      pr_number=$2
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

if [[ -z "$expected_head" || -z "$profile" ]]; then
  echo "ERROR: --expected-head and --profile are required" >&2
  usage >&2
  exit 64
fi

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "ERROR: not inside a Git worktree" >&2
  exit 1
}
cd "$repo_root"
source scripts/agent-runtime/lib.sh
scripts/agent-runtime/bootstrap.sh --check >/dev/null

expected_head=$(git rev-parse --verify "$expected_head^{commit}" 2>/dev/null) || {
  echo "ERROR: expected HEAD is not a commit" >&2
  exit 1
}
current_head=$(git rev-parse HEAD)
if [[ "$current_head" != "$expected_head" ]]; then
  echo "ERROR: HEAD $current_head does not match expected $expected_head" >&2
  exit 1
fi

status=$(git status --porcelain=v1 --untracked-files=all)
if [[ -n "$status" ]]; then
  echo "ERROR: closeout requires a clean worktree" >&2
  printf '%s\n' "$status" >&2
  exit 1
fi

git rev-parse --verify --quiet "$base_ref^{commit}" >/dev/null || {
  echo "ERROR: base is not a commit: $base_ref" >&2
  exit 1
}

receipt_dir="$(git rev-parse --git-common-dir)/agent-runtime/receipts"
if [[ ! -d "$receipt_dir" ]]; then
  echo "ERROR: verification receipt directory does not exist" >&2
  exit 1
fi
if [[ -z "$receipt_path" ]]; then
  short_head=${expected_head:0:12}
  receipt_path=$(find "$receipt_dir" -maxdepth 1 -type f \
    -name "*-$short_head-$profile-EXACT_HEAD-*.receipt" -print 2>/dev/null |
    LC_ALL=C sort | tail -n 1)
fi
if [[ -z "$receipt_path" || ! -f "$receipt_path" || -L "$receipt_path" ]]; then
  echo "ERROR: exact-HEAD receipt not found or not a regular file" >&2
  exit 1
fi

receipt_schema=$(runtime_receipt_value "$receipt_path" schema_version)
receipt_result=$(runtime_receipt_value "$receipt_path" result)
receipt_scope=$(runtime_receipt_value "$receipt_path" scope)
receipt_profile=$(runtime_receipt_value "$receipt_path" profile)
receipt_head=$(runtime_receipt_value "$receipt_path" verified_sha)
receipt_workspace=$(runtime_receipt_value "$receipt_path" workspace_fingerprint)
receipt_dependencies=$(runtime_receipt_value "$receipt_path" dependency_fingerprint)
receipt_git_version=$(runtime_receipt_value "$receipt_path" git_version)
receipt_go_version=$(runtime_receipt_value "$receipt_path" go_version)
receipt_bun_version=$(runtime_receipt_value "$receipt_path" bun_version)

[[ "$receipt_schema" == "1" ]] || {
  echo "ERROR: unsupported receipt schema: $receipt_schema" >&2
  exit 1
}
[[ "$receipt_result" == "passed" && "$receipt_scope" == "EXACT_HEAD" ]] || {
  echo "ERROR: receipt is not a passed exact-HEAD verification" >&2
  exit 1
}
[[ "$receipt_profile" == "$profile" ]] || {
  echo "ERROR: receipt profile $receipt_profile does not match $profile" >&2
  exit 1
}
[[ "$receipt_head" == "$expected_head" ]] || {
  echo "ERROR: receipt HEAD $receipt_head does not match $expected_head" >&2
  exit 1
}
[[ "$receipt_workspace" == "$(runtime_workspace_fingerprint)" ]] || {
  echo "ERROR: workspace fingerprint no longer matches receipt" >&2
  exit 1
}
[[ "$receipt_dependencies" == "$(runtime_dependency_fingerprint)" ]] || {
  echo "ERROR: dependency/config fingerprint no longer matches receipt" >&2
  exit 1
}
[[ "$receipt_git_version" == "$(git version)" ]] || {
  echo "ERROR: Git version no longer matches receipt" >&2
  exit 1
}
[[ "$receipt_go_version" == "$(runtime_profile_go_version "$profile")" ]] || {
  echo "ERROR: Go version no longer matches receipt" >&2
  exit 1
}
[[ "$receipt_bun_version" == "$(runtime_profile_bun_version "$profile")" ]] || {
  echo "ERROR: Bun version no longer matches receipt" >&2
  exit 1
}

git diff --check "$base_ref...HEAD"
read -r base_only head_only <<< "$(git rev-list --left-right --count "$base_ref...HEAD")"

pr_url=NOT_REQUESTED
pr_state=NOT_REQUESTED
pr_merge_state=NOT_REQUESTED
if [[ -n "$pr_number" ]]; then
  command -v gh >/dev/null || {
    echo "ERROR: gh is required for PR closeout" >&2
    exit 1
  }
  pr_values=$(gh pr view "$pr_number" --repo bowenQT/new-api \
    --json headRefOid,baseRefName,state,isDraft,mergeable,mergeStateStatus,url \
    --jq '[.headRefOid, .baseRefName, .state, (.isDraft | tostring), .mergeable, .mergeStateStatus, .url] | @tsv')
  IFS=$'\t' read -r pr_head pr_base pr_state pr_draft pr_mergeable pr_merge_state pr_url <<< "$pr_values"
  [[ "$pr_head" == "$expected_head" ]] || {
    echo "ERROR: PR head $pr_head does not match $expected_head" >&2
    exit 1
  }
  [[ "$pr_base" == "downstream/main" ]] || {
    echo "ERROR: downstream PR base must be downstream/main, found $pr_base" >&2
    exit 1
  }
  [[ "$pr_draft" == "false" ]] || {
    echo "ERROR: PR is still a draft" >&2
    exit 1
  }
  [[ "$pr_state" == "OPEN" || "$pr_state" == "MERGED" ]] || {
    echo "ERROR: PR state is $pr_state" >&2
    exit 1
  }
  [[ "$pr_mergeable" == "MERGEABLE" || "$pr_state" == "MERGED" ]] || {
    echo "ERROR: PR mergeability is $pr_mergeable" >&2
    exit 1
  }
  gh pr checks "$pr_number" --repo bowenQT/new-api
fi

printf 'closeout=PASSED\n'
printf 'verified_sha=%s\n' "$expected_head"
printf 'profile=%s\n' "$profile"
printf 'receipt=%s\n' "$receipt_path"
printf 'receipt_hash=%s\n' "$(git hash-object -- "$receipt_path")"
printf 'base_ref=%s\n' "$base_ref"
printf 'base_only_commits=%s\n' "$base_only"
printf 'head_only_commits=%s\n' "$head_only"
printf 'pr_state=%s\n' "$pr_state"
printf 'pr_merge_state=%s\n' "$pr_merge_state"
printf 'pr_url=%s\n' "$pr_url"
printf 'database=NOT_CLAIMED\n'
printf 'deployment=NOT_CLAIMED\n'
printf 'production=NOT_CLAIMED\n'
printf 'worktree_disposition=KEEP\n'
