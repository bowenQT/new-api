package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type forkDeltaLedger struct {
	Version int         `yaml:"version"`
	Entries []forkDelta `yaml:"entries"`
}

type forkDelta struct {
	ID                  string   `yaml:"id"`
	Status              string   `yaml:"status"`
	Owner               string   `yaml:"owner"`
	IntroducedCommit    string   `yaml:"introduced_commit"`
	Purpose             string   `yaml:"purpose"`
	Paths               []string `yaml:"paths"`
	UpstreamTouchpoints []string `yaml:"upstream_touchpoints"`
	ConflictRisk        string   `yaml:"conflict_risk"`
	Verification        []string `yaml:"verification"`
	RemovalCondition    string   `yaml:"removal_condition"`
	UpstreamReference   *string  `yaml:"upstream_reference"`
}

var (
	deltaIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func main() {
	ledgerPath := flag.String("file", "docs/downstream/fork-deltas.yaml", "fork delta ledger")
	baseRef := flag.String("base", "upstream/main", "base ref for downstream path coverage")
	flag.Parse()

	repoRoot, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: resolve repository root: %v\n", err)
		os.Exit(1)
	}
	if err := os.Chdir(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: enter repository root: %v\n", err)
		os.Exit(1)
	}

	ledger, err := readForkDeltaLedger(*ledgerPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: read fork delta ledger: %v\n", err)
		os.Exit(1)
	}

	diffRange := *baseRef + "...HEAD"
	changedPaths, deletedPathList, diffPathsErr := loadForkDeltaPaths(diffRange)
	deletedPaths := make(map[string]struct{}, len(deletedPathList))
	for _, path := range deletedPathList {
		deletedPaths[path] = struct{}{}
	}

	problems := validateForkDeltaLedger(ledger, repoRoot, deletedPaths, func(commit string) error {
		if _, err := gitOutput("cat-file", "-e", commit+"^{commit}"); err != nil {
			return errors.New("commit is not available")
		}
		if err := exec.Command("git", "merge-base", "--is-ancestor", commit, "HEAD").Run(); err != nil {
			return errors.New("commit is not an ancestor of HEAD")
		}
		return nil
	})

	if diffPathsErr != nil {
		problems = append(problems, fmt.Sprintf("changed paths: %v", diffPathsErr))
	} else {
		problems = append(problems, validateChangedPathCoverage(ledger, changedPaths)...)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", problem)
		}
		os.Exit(1)
	}

	fmt.Printf("fork_delta_validation=PASSED\nentries=%d\n", len(ledger.Entries))
}

func readForkDeltaLedger(path string) (forkDeltaLedger, error) {
	file, err := os.Open(path)
	if err != nil {
		return forkDeltaLedger{}, err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var ledger forkDeltaLedger
	if err := decoder.Decode(&ledger); err != nil {
		return forkDeltaLedger{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return forkDeltaLedger{}, errors.New("multiple YAML documents are not allowed")
		}
		return forkDeltaLedger{}, err
	}
	return ledger, nil
}

func validateForkDeltaLedger(
	ledger forkDeltaLedger,
	repoRoot string,
	deletedPaths map[string]struct{},
	validateCommit func(string) error,
) []string {
	var problems []string
	if ledger.Version != 1 {
		problems = append(problems, fmt.Sprintf("version must be 1, found %d", ledger.Version))
	}
	if len(ledger.Entries) == 0 {
		problems = append(problems, "entries must not be empty")
	}

	seenIDs := make(map[string]struct{}, len(ledger.Entries))
	seenPaths := make(map[string]string)
	for index, entry := range ledger.Entries {
		label := fmt.Sprintf("entries[%d]", index)
		if !deltaIDPattern.MatchString(entry.ID) {
			problems = append(problems, label+".id must be lowercase kebab-case")
		}
		if _, exists := seenIDs[entry.ID]; exists {
			problems = append(problems, label+".id is duplicated: "+entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}
		if entry.Status != "active" && entry.Status != "retired" {
			problems = append(problems, label+".status must be active or retired")
		}
		if strings.TrimSpace(entry.Owner) == "" {
			problems = append(problems, label+".owner is required")
		}
		if !commitPattern.MatchString(entry.IntroducedCommit) {
			problems = append(problems, label+".introduced_commit must be a full lowercase SHA")
		} else if err := validateCommit(entry.IntroducedCommit); err != nil {
			problems = append(problems, label+".introduced_commit "+err.Error())
		}
		if strings.TrimSpace(entry.Purpose) == "" {
			problems = append(problems, label+".purpose is required")
		}
		if len(entry.Paths) == 0 {
			problems = append(problems, label+".paths must not be empty")
		}
		for _, path := range entry.Paths {
			if path == "" || filepath.IsAbs(path) || strings.Contains(path, "..") {
				problems = append(problems, label+".paths contains an unsafe path: "+path)
				continue
			}
			if priorID, exists := seenPaths[path]; exists {
				problems = append(problems, fmt.Sprintf("path %s is owned by both %s and %s", path, priorID, entry.ID))
			}
			seenPaths[path] = entry.ID
			if entry.Status == "active" {
				if _, err := os.Stat(filepath.Join(repoRoot, path)); err != nil {
					_, isDeleted := deletedPaths[path]
					if !errors.Is(err, os.ErrNotExist) || !isDeleted {
						problems = append(problems, label+".paths does not exist: "+path)
					}
				}
			}
		}
		if len(entry.UpstreamTouchpoints) == 0 {
			problems = append(problems, label+".upstream_touchpoints must not be empty")
		}
		if entry.ConflictRisk != "low" && entry.ConflictRisk != "medium" && entry.ConflictRisk != "high" {
			problems = append(problems, label+".conflict_risk must be low, medium, or high")
		}
		if len(entry.Verification) == 0 {
			problems = append(problems, label+".verification must not be empty")
		}
		if strings.TrimSpace(entry.RemovalCondition) == "" {
			problems = append(problems, label+".removal_condition is required")
		}
	}
	return problems
}

func validateChangedPathCoverage(ledger forkDeltaLedger, changedPaths []string) []string {
	coveredPaths := make(map[string]struct{})
	for _, entry := range ledger.Entries {
		if entry.Status != "active" {
			continue
		}
		for _, path := range entry.Paths {
			coveredPaths[path] = struct{}{}
		}
	}

	var problems []string
	for _, path := range changedPaths {
		if path == "" || isGovernancePath(path) {
			continue
		}
		if _, covered := coveredPaths[path]; !covered {
			problems = append(problems, "downstream path has no active fork-delta entry: "+path)
		}
	}
	return problems
}

func isGovernancePath(path string) bool {
	return strings.HasPrefix(path, "docs/downstream/") ||
		strings.HasPrefix(path, ".agents/runtime/") ||
		strings.HasPrefix(path, ".agents/skills/upstream-sync/") ||
		strings.HasPrefix(path, "scripts/agent-runtime/")
}

func gitOutput(args ...string) (string, error) {
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func gitNullLines(args ...string) ([]string, error) {
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	if len(output) == 0 {
		return nil, nil
	}
	parts := strings.Split(string(output), "\x00")
	return parts[:len(parts)-1], nil
}

func loadForkDeltaPaths(diffRange string) ([]string, []string, error) {
	changedPaths, err := gitNullLines("diff", "--no-renames", "--name-only", "-z", diffRange)
	if err != nil {
		return nil, nil, err
	}
	deletedPaths, err := gitNullLines("diff", "--no-renames", "--diff-filter=D", "--name-only", "-z", diffRange)
	if err != nil {
		return nil, nil, err
	}
	return changedPaths, deletedPaths, nil
}
