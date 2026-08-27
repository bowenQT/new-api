package main

import (
	"os"
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

	problems := validateForkDeltaLedger(ledger, repoRoot, func(string) error { return nil })
	assert.Contains(t, problems, "entries[1].id is duplicated: runtime-hook")
	assert.Contains(t, problems, "entries[1].status must be active or retired")
	assert.Contains(t, problems, "entries[1].owner is required")
	assert.Contains(t, problems, "path hook.go is owned by both runtime-hook and runtime-hook")
}

func TestValidateChangedPathCoverage(t *testing.T) {
	ledger := forkDeltaLedger{
		Entries: []forkDelta{{
			ID:     "runtime-hook",
			Status: "active",
			Paths:  []string{"AGENTS.md"},
		}},
	}

	problems := validateChangedPathCoverage(ledger, []string{
		"AGENTS.md",
		"docs/downstream/development.md",
		"service/custom.go",
	})
	require.Len(t, problems, 1)
	assert.Equal(t, "downstream path has no active fork-delta entry: service/custom.go", problems[0])
}
