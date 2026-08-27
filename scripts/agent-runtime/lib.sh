#!/usr/bin/env bash

# Shared, read-only evidence functions for verify.sh and closeout.sh.

runtime_workspace_fingerprint() {
  {
    git diff --binary --no-ext-diff
    git diff --cached --binary --no-ext-diff
    while IFS= read -r -d '' untracked_path; do
      printf 'untracked:%s\n' "$untracked_path"
      git hash-object -- "$untracked_path"
    done < <(git ls-files --others --exclude-standard -z)
  } | git hash-object --stdin
}

runtime_dependency_fingerprint() {
  local dependency_file
  for dependency_file in \
    go.mod go.sum relaykit/go.mod relaykit/go.sum web/bun.lock makefile \
    .github/workflows/ci.yml Dockerfile deploy/sgp1/docker-compose.yml; do
    if [[ -f "$dependency_file" ]]; then
      printf '%s:%s\n' "$dependency_file" "$(git hash-object -- "$dependency_file")"
    fi
  done | git hash-object --stdin
}

runtime_profile_go_version() {
  local profile=$1
  case "$profile" in
    docs | backend | relaykit | full | database)
      go version
      ;;
    *)
      printf 'not-run\n'
      ;;
  esac
}

runtime_profile_bun_version() {
  local profile=$1
  case "$profile" in
    frontend | full)
      bun --version
      ;;
    *)
      printf 'not-run\n'
      ;;
  esac
}

runtime_profile_docker_compose_version() {
  local profile=$1
  case "$profile" in
    deployment)
      docker compose version
      ;;
    *)
      printf 'not-run\n'
      ;;
  esac
}

runtime_receipt_value() {
  local receipt_path=$1
  local key=$2
  local count
  local value
  count=$(awk -F '\t' -v wanted="$key" '$1 == wanted { count += 1 } END { print count + 0 }' "$receipt_path")
  if [[ "$count" != "1" ]]; then
    echo "ERROR: receipt key '$key' occurs $count times" >&2
    return 1
  fi
  value=$(awk -F '\t' -v wanted="$key" '$1 == wanted { print substr($0, index($0, "\t") + 1) }' "$receipt_path")
  printf '%s\n' "$value"
}

runtime_require_go_test_pass_event() {
  local events_path=$1
  local test_name=$2

  if ! awk -v required_test="$test_name" '
    index($0, "\"Action\":\"pass\"") &&
      index($0, "\"Test\":\"" required_test "\"") {
        passed = 1
      }
    END { exit passed ? 0 : 1 }
  ' "$events_path"; then
    echo "ERROR: required Go test did not pass (a skip is not sufficient): $test_name" >&2
    return 1
  fi
}

runtime_validate_pr_delivery_state() {
  local pr_state=$1
  local pr_mergeable=$2
  local pr_merge_state=$3
  local pr_review_decision=$4

  if [[ "$pr_state" == "MERGED" ]]; then
    return
  fi
  if [[ "$pr_state" != "OPEN" ]]; then
    echo "ERROR: PR state is $pr_state" >&2
    return 1
  fi
  if [[ "$pr_mergeable" != "MERGEABLE" ]]; then
    echo "ERROR: PR mergeability is $pr_mergeable" >&2
    return 1
  fi
  if [[ "$pr_merge_state" != "CLEAN" ]]; then
    echo "ERROR: PR protection state is $pr_merge_state" >&2
    return 1
  fi
  if [[ "$pr_review_decision" == "CHANGES_REQUESTED" ]]; then
    echo "ERROR: PR review decision is CHANGES_REQUESTED" >&2
    return 1
  fi
}
