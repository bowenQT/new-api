package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateForkDeltaLedgerRejectsDuplicateAndMissingContracts(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "hook.go"), []byte("package hook\n"), 0o600))

	ledger := forkDeltaLedger{
		Version: 1,
		Entries: []forkDelta{
			{
				ID:                  "runtime-hook",
				Status:              "active",
				Owner:               "maintainers",
				IntroducedCommit:    "1111111111111111111111111111111111111111",
				Purpose:             "Connect downstream behavior.",
				Paths:               []string{"hook.go"},
				UpstreamTouchpoints: []string{"router"},
				ConflictRisk:        "medium",
				Verification:        []string{"go test ./..."},
				RemovalCondition:    "Remove when upstream supports the behavior.",
			},
			{ID: "runtime-hook", Status: "unknown", Paths: []string{"hook.go"}},
		},
	}

	problems := validateForkDeltaLedger(ledger, repoRoot, nil, func(string) error { return nil })
	assert.Contains(t, problems, "entries[1].id is duplicated: runtime-hook")
	assert.Contains(t, problems, "entries[1].status must be active or retired")
	assert.Contains(t, problems, "entries[1].owner is required")
	assert.Contains(t, problems, "path hook.go is owned by both runtime-hook and runtime-hook")
}

func TestValidateForkDeltaLedgerAllowsActiveDeletedPaths(t *testing.T) {
	ledger := forkDeltaLedger{
		Version: 1,
		Entries: []forkDelta{{
			ID:                  "removed-upstream-hook",
			Status:              "active",
			Owner:               "maintainers",
			IntroducedCommit:    "1111111111111111111111111111111111111111",
			Purpose:             "Remove an upstream-owned hook downstream.",
			Paths:               []string{"removed.go"},
			UpstreamTouchpoints: []string{"upstream hook"},
			ConflictRisk:        "medium",
			Verification:        []string{"go test ./..."},
			RemovalCondition:    "Remove when upstream removes the hook.",
		}},
	}

	problems := validateForkDeltaLedger(
		ledger,
		t.TempDir(),
		map[string]struct{}{"removed.go": {}},
		func(string) error { return nil },
	)
	assert.Empty(t, problems)

	problems = validateForkDeltaLedger(ledger, t.TempDir(), nil, func(string) error { return nil })
	assert.Contains(t, problems, "entries[0].paths does not exist: removed.go")
}

func TestValidateChangedPathCoverage(t *testing.T) {
	ledger := forkDeltaLedger{
		Entries: []forkDelta{{
			ID:     "runtime-hook",
			Status: "active",
			Paths:  []string{"AGENTS.md", "removed.go"},
		}},
	}

	problems := validateChangedPathCoverage(ledger, []string{
		"AGENTS.md",
		"removed.go",
		"docs/downstream/development.md",
		"service/custom.go",
	})
	require.Len(t, problems, 1)
	assert.Equal(t, "downstream path has no active fork-delta entry: service/custom.go", problems[0])
}

func TestLoadForkDeltaPathsTreatsRenamesAsDeleteAndAdd(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", repoRoot, "init", "--quiet").Run())
	require.NoError(t, exec.Command("git", "-C", repoRoot, "config", "user.name", "RuntimeTest").Run())
	require.NoError(t, exec.Command("git", "-C", repoRoot, "config", "user.email", "runtime@example.invalid").Run())
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "old.go"), []byte("package old\n"), 0o600))
	require.NoError(t, exec.Command("git", "-C", repoRoot, "add", "--", "old.go").Run())
	require.NoError(t, exec.Command("git", "-C", repoRoot, "commit", "--quiet", "-m", "base").Run())
	baseOutput, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	baseRef := string(baseOutput[:len(baseOutput)-1])
	require.NoError(t, os.Rename(filepath.Join(repoRoot, "old.go"), filepath.Join(repoRoot, "new.go")))
	require.NoError(t, exec.Command("git", "-C", repoRoot, "add", "--all").Run())
	require.NoError(t, exec.Command("git", "-C", repoRoot, "commit", "--quiet", "-m", "rename").Run())

	originalWorkingDirectory, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWorkingDirectory))
	})

	for _, renameSetting := range []string{"true", "false"} {
		require.NoError(t, exec.Command("git", "config", "diff.renames", renameSetting).Run())
		changedPaths, deletedPaths, err := loadForkDeltaPaths(baseRef + "...HEAD")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"old.go", "new.go"}, changedPaths)
		assert.Equal(t, []string{"old.go"}, deletedPaths)
	}
}
