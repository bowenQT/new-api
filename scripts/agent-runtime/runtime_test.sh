#!/usr/bin/env bash

set -euo pipefail

source_root=$(git rev-parse --show-toplevel)
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/newapi-runtime-test.XXXXXX")
cleanup() {
  rm -rf "$fixture_root"
}
trap cleanup EXIT

expect_failure() {
  local expected_text=$1
  shift
  local output_file="$fixture_root/failure-output"
  set +e
  "$@" > "$output_file" 2>&1
  local command_exit=$?
  set -e
  if [[ "$command_exit" == "0" ]]; then
    echo "ERROR: command unexpectedly succeeded: $*" >&2
    exit 1
  fi
  if ! grep -Fq "$expected_text" "$output_file"; then
    echo "ERROR: failure did not contain '$expected_text': $*" >&2
    cat "$output_file" >&2
    exit 1
  fi
}

source "$source_root/scripts/agent-runtime/lib.sh"

test_events="$fixture_root/test-events.jsonl"
printf '%s\n' \
  '{"Action":"pass","Package":"example.invalid/project/model","Test":"TestDatabaseContract/mysql"}' \
  '{"Action":"skip","Package":"example.invalid/project/model","Test":"TestDatabaseContract/postgres"}' \
  > "$test_events"
runtime_require_go_test_pass_event "$test_events" 'TestDatabaseContract/mysql'
expect_failure "required Go test did not pass" \
  runtime_require_go_test_pass_event "$test_events" 'TestDatabaseContract/postgres'

run_in_repo() {
  local repo_path=$1
  shift
  (
    cd "$repo_path"
    "$@"
  )
}

init_fixture() {
  local repo_path=$1
  mkdir -p "$repo_path/scripts/agent-runtime"
  cp "$source_root/scripts/agent-runtime/bootstrap.sh" "$repo_path/scripts/agent-runtime/"
  cp "$source_root/scripts/agent-runtime/preflight.sh" "$repo_path/scripts/agent-runtime/"
  cp "$source_root/scripts/agent-runtime/upstream-audit.sh" "$repo_path/scripts/agent-runtime/"
  chmod +x "$repo_path/scripts/agent-runtime/"*.sh
  git -C "$repo_path" init --quiet
  git -C "$repo_path" config user.name RuntimeTest
  git -C "$repo_path" config user.email runtime-test@example.invalid
  printf '.env\n' > "$repo_path/.gitignore"
  printf 'fixture\n' > "$repo_path/README.md"
  git -C "$repo_path" add .gitignore README.md scripts/agent-runtime
  git -C "$repo_path" commit --quiet -m fixture
  git -C "$repo_path" branch -M main
  git -C "$repo_path" remote add origin https://github.com/bowenQT/new-api.git
  git -C "$repo_path" remote add upstream https://github.com/QuantumNous/new-api.git
}

malicious_repo="$fixture_root/malicious"
init_fixture "$malicious_repo"
git -C "$malicious_repo" remote set-url origin https://evilgithub.com/bowenQT/new-api.git
expect_failure "unexpected origin URL" \
  run_in_repo "$malicious_repo" scripts/agent-runtime/bootstrap.sh --apply

git -C "$malicious_repo" remote set-url origin https://github.com/bowenQT/new-api.git
git -C "$malicious_repo" remote set-url --add --push origin https://evil.example/github.com/bowenQT/new-api.git
expect_failure "unexpected origin URL" \
  run_in_repo "$malicious_repo" scripts/agent-runtime/bootstrap.sh --apply

git -C "$malicious_repo" config --unset-all remote.origin.pushurl
git -C "$malicious_repo" config url.https://evil.example/.pushInsteadOf https://github.com/
expect_failure "unexpected effective origin push URL" \
  run_in_repo "$malicious_repo" scripts/agent-runtime/bootstrap.sh --apply
git -C "$malicious_repo" config --unset-all url.https://evil.example/.pushInsteadOf

git -C "$malicious_repo" config url.https://evil.example/.insteadOf https://github.com/
expect_failure "unexpected effective origin fetch URL" \
  run_in_repo "$malicious_repo" scripts/agent-runtime/bootstrap.sh --apply
git -C "$malicious_repo" config --unset-all url.https://evil.example/.insteadOf

fixture_repo="$fixture_root/valid"
init_fixture "$fixture_repo"
git -C "$fixture_repo" branch downstream/main
git -C "$fixture_repo" update-ref refs/remotes/origin/main refs/heads/main
git -C "$fixture_repo" update-ref refs/remotes/upstream/main refs/heads/main
git -C "$fixture_repo" update-ref refs/remotes/origin/downstream/main refs/heads/downstream/main
(
  cd "$fixture_repo"
  scripts/agent-runtime/bootstrap.sh --apply >/dev/null
  scripts/agent-runtime/bootstrap.sh --check >/dev/null
)

git -C "$fixture_repo" switch --quiet downstream/main
expect_failure "edit mode requires a feature or maintenance branch" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit

git -C "$fixture_repo" switch --quiet -c feature/runtime-test
printf 'placeholder\n' > "$fixture_repo/.env"
expect_failure "ignored credential/data artifact" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
rm -f "$fixture_repo/.env"

printf 'dirty\n' >> "$fixture_repo/README.md"
expect_failure "sync mode requires a clean worktree" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/upstream-audit.sh --no-fetch --target HEAD

printf 'runtime_tests=PASSED\n'
