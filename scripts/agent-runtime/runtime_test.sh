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

fake_bin="$fixture_root/fake-bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'printf "Docker Compose version v9.9.9-test\\n"' > "$fake_bin/docker"
chmod +x "$fake_bin/docker"
if [[ "$(PATH="$fake_bin:$PATH" runtime_profile_docker_compose_version deployment)" != "Docker Compose version v9.9.9-test" ]]; then
  echo "ERROR: deployment toolchain version was not captured" >&2
  exit 1
fi
if [[ "$(runtime_profile_docker_compose_version docs)" != "not-run" ]]; then
  echo "ERROR: non-deployment profile unexpectedly captured Docker Compose" >&2
  exit 1
fi

runtime_validate_pr_delivery_state OPEN MERGEABLE CLEAN NONE
expect_failure "PR protection state is BLOCKED" \
  runtime_validate_pr_delivery_state OPEN MERGEABLE BLOCKED NONE
expect_failure "PR review decision is CHANGES_REQUESTED" \
  runtime_validate_pr_delivery_state OPEN MERGEABLE CLEAN CHANGES_REQUESTED
runtime_validate_pr_delivery_state MERGED UNKNOWN UNKNOWN CHANGES_REQUESTED

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
  mkdir -p "$repo_path/pkg/billingexpr"
  cp "$source_root/scripts/agent-runtime/bootstrap.sh" "$repo_path/scripts/agent-runtime/"
  cp "$source_root/scripts/agent-runtime/preflight.sh" "$repo_path/scripts/agent-runtime/"
  cp "$source_root/scripts/agent-runtime/upstream-audit.sh" "$repo_path/scripts/agent-runtime/"
  chmod +x "$repo_path/scripts/agent-runtime/"*.sh
  git -C "$repo_path" init --quiet
  git -C "$repo_path" config user.name RuntimeTest
  git -C "$repo_path" config user.email runtime-test@example.invalid
  printf '.env\n' > "$repo_path/.gitignore"
  printf 'fixture\n' > "$repo_path/README.md"
  printf 'fixture\n' > "$repo_path/pkg/billingexpr/rename-source.go"
  git -C "$repo_path" add .gitignore README.md scripts/agent-runtime pkg/billingexpr/rename-source.go
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
git -C "$fixture_repo" switch --quiet downstream/main
printf 'downstream-only\n' > "$fixture_repo/downstream.txt"
git -C "$fixture_repo" add downstream.txt
git -C "$fixture_repo" commit --quiet -m downstream
git -C "$fixture_repo" switch --quiet -c feature/stale-downstream
printf 'stale-topic\n' > "$fixture_repo/stale-topic.txt"
git -C "$fixture_repo" add stale-topic.txt
git -C "$fixture_repo" commit --quiet -m stale-topic
git -C "$fixture_repo" switch --quiet downstream/main
printf 'downstream-later\n' > "$fixture_repo/downstream-later.txt"
git -C "$fixture_repo" add downstream-later.txt
git -C "$fixture_repo" commit --quiet -m downstream-later
git -C "$fixture_repo" switch --quiet main
git -C "$fixture_repo" update-ref refs/remotes/origin/main refs/heads/main
git -C "$fixture_repo" update-ref refs/remotes/upstream/main refs/heads/main
git -C "$fixture_repo" update-ref refs/remotes/origin/downstream/main refs/heads/downstream/main
(
  cd "$fixture_repo"
  scripts/agent-runtime/bootstrap.sh --apply >/dev/null
  scripts/agent-runtime/bootstrap.sh --check >/dev/null
)
if [[ "$(git -C "$fixture_repo" config --local --get branch.main.remote)" != "origin" ||
  "$(git -C "$fixture_repo" config --local --get branch.main.merge)" != "refs/heads/main" ]]; then
  echo "ERROR: bootstrap did not configure main to track origin/main" >&2
  exit 1
fi
git -C "$fixture_repo" config --local branch.main.merge refs/heads/downstream/main
expect_failure "branch.main.merge expected exactly one value 'refs/heads/main'" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --apply >/dev/null
git -C "$fixture_repo" config --local --unset-all branch.main.merge
git -C "$fixture_repo" config --local --add branch.main.merge refs/heads/downstream/main
git -C "$fixture_repo" config --local --add branch.main.merge refs/heads/main
expect_failure "branch.main.merge expected exactly one value 'refs/heads/main'" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$fixture_repo" config --local --unset-all branch.main.merge
run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --apply >/dev/null

git -C "$fixture_repo" config --local extensions.worktreeConfig true
git -C "$fixture_repo" config --worktree push.default current
expect_failure "effective push.default expected 'simple'" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --apply
git -C "$fixture_repo" config --worktree --unset push.default
git -C "$fixture_repo" config --worktree branch.main.merge refs/heads/downstream/main
expect_failure "effective branch.main.merge expected exactly one value 'refs/heads/main'" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$fixture_repo" config --worktree --unset branch.main.merge
run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check >/dev/null

linked_worktree="$fixture_root/valid-linked"
git -C "$fixture_repo" worktree add --quiet -b feature/linked "$linked_worktree" downstream/main
git -C "$linked_worktree" config --worktree remote.origin.url https://evil.example/bowenQT/new-api.git
expect_failure "unexpected effective origin fetch URL in worktree" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$linked_worktree" config --worktree --unset remote.origin.url
git -C "$linked_worktree" config --worktree url.https://evil.example/.insteadOf DISABLED
expect_failure "effective upstream push URL must be exactly DISABLED in worktree" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$linked_worktree" config --worktree --unset url.https://evil.example/.insteadOf
git -C "$linked_worktree" config --worktree push.default current
expect_failure "effective push.default expected 'simple'" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$linked_worktree" config --worktree --unset push.default
git -C "$linked_worktree" config --worktree branch.main.merge refs/heads/downstream/main
expect_failure "effective branch.main.merge expected exactly one value 'refs/heads/main'" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$linked_worktree" config --worktree --unset branch.main.merge
run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check >/dev/null

global_config="$fixture_root/global-config"
git config --file "$global_config" push.default simple
GIT_CONFIG_GLOBAL="$global_config" run_in_repo "$fixture_repo" \
  scripts/agent-runtime/bootstrap.sh --check >/dev/null

git -C "$fixture_repo" switch --quiet downstream/main
main_branch_config="$fixture_root/main-branch-config"
git config --file "$main_branch_config" branch.main.remote upstream
git config --file "$main_branch_config" branch.main.merge refs/heads/downstream/main
git -C "$fixture_repo" config --local includeIf.onbranch:main.path "$main_branch_config"
expect_failure "branch-conditioned safety override" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/bootstrap.sh --check
git -C "$fixture_repo" config --local --unset includeIf.onbranch:main.path
expect_failure "edit mode requires a symbolic topic branch" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit

git -C "$fixture_repo" switch --quiet --detach
expect_failure "edit mode requires a symbolic topic branch" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
git -C "$fixture_repo" switch --quiet downstream/main

git -C "$fixture_repo" switch --quiet -c feature/runtime-test
printf 'placeholder\n' > "$fixture_repo/.env"
expect_failure "ignored credential/data artifact" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
rm -f "$fixture_repo/.env"
printf 'placeholder\n' > "$fixture_repo/a b.pem"
expect_failure "suspicious uncommitted credential/data artifact: a b.pem" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
rm -f "$fixture_repo/a b.pem"
printf 'placeholder\n' > "$fixture_repo/source key.pem"
git -C "$fixture_repo" add -- 'source key.pem'
git -C "$fixture_repo" commit --quiet -m sensitive-rename-fixture
git -C "$fixture_repo" mv -- 'source key.pem' safe-renamed.txt
expect_failure "suspicious uncommitted credential/data artifact: source key.pem" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
git -C "$fixture_repo" mv -- safe-renamed.txt 'source key.pem'
run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit >/dev/null

mkdir -p "$fixture_repo/docs"
git -C "$fixture_repo" mv -- pkg/billingexpr/rename-source.go docs/renamed.md
git -C "$fixture_repo" commit --quiet -m rename-high-risk-path

mkdir -p \
  "$fixture_repo/pkg/billingexpr" \
  "$fixture_repo/oauth" \
  "$fixture_repo/i18n" \
  "$fixture_repo/constant" \
  "$fixture_repo/web/src/routes" \
  "$fixture_repo/docs"
printf 'fixture\n' > "$fixture_repo/pkg/billingexpr/runtime-review.go"
printf 'fixture\n' > "$fixture_repo/oauth/runtime-review.go"
printf 'fixture\n' > "$fixture_repo/i18n/runtime-review.go"
printf 'fixture\n' > "$fixture_repo/constant/runtime-review.go"
printf 'fixture\n' > "$fixture_repo/web/src/routes/runtime-review.tsx"
printf 'fixture\n' > "$fixture_repo/docs/ordinary.md"
git -C "$fixture_repo" add -- \
  pkg/billingexpr/runtime-review.go \
  oauth/runtime-review.go \
  i18n/runtime-review.go \
  constant/runtime-review.go \
  web/src/routes/runtime-review.tsx \
  docs/ordinary.md
git -C "$fixture_repo" commit --quiet -m high-risk-paths
audit_output="$fixture_root/upstream-audit-output"
run_in_repo "$fixture_repo" scripts/agent-runtime/upstream-audit.sh --no-fetch --target HEAD > "$audit_output"
semantic_paths=$(awk '/^semantic_review_paths:$/ { capture = 1; next } capture { print }' "$audit_output")
for high_risk_path in \
  pkg/billingexpr/rename-source.go \
  pkg/billingexpr/runtime-review.go \
  oauth/runtime-review.go \
  i18n/runtime-review.go \
  constant/runtime-review.go \
  web/src/routes/runtime-review.tsx; do
  if ! grep -Fxq "$high_risk_path" <<< "$semantic_paths"; then
    echo "ERROR: semantic review omitted high-risk path: $high_risk_path" >&2
    exit 1
  fi
done
if grep -Fxq 'docs/ordinary.md' <<< "$semantic_paths"; then
  echo "ERROR: semantic review included an ordinary docs path" >&2
  exit 1
fi

expect_failure "contains downstream-only history" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode upstream-edit

git -C "$fixture_repo" switch --quiet main
git -C "$fixture_repo" switch --quiet -c feature/upstream-test
expect_failure "does not descend from origin/downstream/main" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode upstream-edit >/dev/null

git -C "$fixture_repo" switch --quiet feature/stale-downstream
expect_failure "contains downstream-only history" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode upstream-edit
git -C "$fixture_repo" switch --quiet feature/runtime-test

git -C "$fixture_repo" update-ref -d refs/remotes/origin/downstream/main
expect_failure "edit mode requires origin/downstream/main" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
expect_failure "upstream-edit mode requires origin/downstream/main" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode upstream-edit
git -C "$fixture_repo" update-ref refs/remotes/origin/downstream/main refs/heads/downstream/main

unrelated_commit=$(git -C "$fixture_repo" commit-tree "$(git -C "$fixture_repo" rev-parse HEAD^{tree})" -m unrelated)
git -C "$fixture_repo" branch feature/unrelated "$unrelated_commit"
git -C "$fixture_repo" switch --quiet feature/unrelated
expect_failure "does not descend from origin/downstream/main" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/preflight.sh --mode edit
git -C "$fixture_repo" switch --quiet feature/runtime-test

printf 'dirty\n' >> "$fixture_repo/README.md"
expect_failure "sync mode requires a clean worktree" \
  run_in_repo "$fixture_repo" scripts/agent-runtime/upstream-audit.sh --no-fetch --target HEAD

printf 'runtime_tests=PASSED\n'
