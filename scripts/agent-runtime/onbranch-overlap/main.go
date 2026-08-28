package main

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

type tokenKind uint8

const (
	literalToken tokenKind = iota
	anyToken
	starToken
	doubleStarToken
	classToken
)

type globToken struct {
	kind    tokenKind
	literal byte
	allowed [256]bool
}

func main() {
	patterns := os.Args[1:]
	if len(patterns) > 0 && patterns[0] == "--" {
		patterns = patterns[1:]
	}
	if len(patterns) == 0 {
		os.Exit(2)
	}

	compiled := make([][]globToken, 0, len(patterns))
	for _, pattern := range patterns {
		tokens, ok := compileGlob(pattern)
		if !ok {
			// Unsupported syntax is treated conservatively as a possible overlap.
			os.Exit(0)
		}
		compiled = append(compiled, tokens)
	}
	if globsOverlap(compiled) {
		os.Exit(0)
	}
	os.Exit(1)
}

func compileGlob(pattern string) ([]globToken, bool) {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	tokens := make([]globToken, 0, len(pattern))
	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '\\':
			if index+1 >= len(pattern) {
				return nil, false
			}
			tokens = append(tokens, globToken{kind: literalToken, literal: pattern[index+1]})
			index += 2
		case '*':
			next := index + 1
			for next < len(pattern) && pattern[next] == '*' {
				next++
			}
			kind := starToken
			if next-index > 1 {
				// Treat every multi-star run as slash-matching. This is a safe
				// over-approximation for uncommon placements outside Git's documented
				// ** forms: it can retain an unreachable include, never skip a reachable one.
				kind = doubleStarToken
			}
			tokens = append(tokens, globToken{kind: kind})
			if kind == doubleStarToken && next < len(pattern) && pattern[next] == '/' {
				// Git's leading and infix **/ forms also match zero directories. Fold
				// the slash into the multi-star token; accepting a few extra strings is
				// conservative for safety validation.
				next++
			}
			index = next
		case '?':
			tokens = append(tokens, globToken{kind: anyToken})
			index++
		case '[':
			token, next, ok := compileClass(pattern, index)
			if !ok {
				return nil, false
			}
			tokens = append(tokens, token)
			index = next
		default:
			tokens = append(tokens, globToken{kind: literalToken, literal: pattern[index]})
			index++
		}
	}
	return tokens, true
}

func compileClass(pattern string, start int) (globToken, int, bool) {
	if strings.HasPrefix(pattern[start:], "[[:") {
		return globToken{}, 0, false
	}
	index := start + 1
	negated := false
	if index < len(pattern) && (pattern[index] == '!' || pattern[index] == '^') {
		negated = true
		index++
	}
	if index >= len(pattern) {
		return globToken{}, 0, false
	}

	var allowed [256]bool
	hasValue := false
	if pattern[index] == ']' {
		allowed[']'] = true
		hasValue = true
		index++
	}
	for index < len(pattern) && pattern[index] != ']' {
		first, next, ok := classByte(pattern, index)
		if !ok {
			return globToken{}, 0, false
		}
		index = next
		if index+1 < len(pattern) && pattern[index] == '-' && pattern[index+1] != ']' {
			last, rangeNext, rangeOK := classByte(pattern, index+1)
			if !rangeOK || first > last {
				return globToken{}, 0, false
			}
			for value := int(first); value <= int(last); value++ {
				allowed[value] = true
			}
			index = rangeNext
		} else {
			allowed[first] = true
		}
		hasValue = true
	}
	if !hasValue || index >= len(pattern) || pattern[index] != ']' {
		return globToken{}, 0, false
	}
	if negated {
		for value := 1; value < len(allowed); value++ {
			allowed[value] = !allowed[value]
		}
	}
	allowed['/'] = false
	return globToken{kind: classToken, allowed: allowed}, index + 1, true
}

func classByte(pattern string, index int) (byte, int, bool) {
	if index >= len(pattern) {
		return 0, 0, false
	}
	if pattern[index] == '\\' {
		if index+1 >= len(pattern) {
			return 0, 0, false
		}
		return pattern[index+1], index + 2, true
	}
	return pattern[index], index + 1, true
}

func globsOverlap(patterns [][]globToken) bool {
	start := make([][]int, len(patterns))
	for index, pattern := range patterns {
		start[index] = epsilonClosure(pattern, []int{0})
	}
	queue := [][][]int{start}
	seen := map[string]struct{}{productKey(start): {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if productAccepts(patterns, current) {
			return true
		}
		for value := 1; value < 256; value++ {
			next := make([][]int, len(patterns))
			possible := true
			for index, pattern := range patterns {
				next[index] = step(pattern, current[index], byte(value))
				if len(next[index]) == 0 {
					possible = false
					break
				}
			}
			if !possible {
				continue
			}
			key := productKey(next)
			if _, exists := seen[key]; exists {
				continue
			}
			// A pathological product should fail open for validation: retaining
			// an unreachable include is safer than skipping a reachable override.
			if len(seen) >= 100000 {
				return true
			}
			seen[key] = struct{}{}
			queue = append(queue, next)
		}
	}
	return false
}

func epsilonClosure(pattern []globToken, states []int) []int {
	set := make(map[int]struct{}, len(states))
	queue := append([]int(nil), states...)
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		if _, exists := set[state]; exists {
			continue
		}
		set[state] = struct{}{}
		if state < len(pattern) && (pattern[state].kind == starToken || pattern[state].kind == doubleStarToken) {
			queue = append(queue, state+1)
		}
	}
	result := make([]int, 0, len(set))
	for state := range set {
		result = append(result, state)
	}
	sort.Ints(result)
	return result
}

func step(pattern []globToken, states []int, value byte) []int {
	next := make([]int, 0, len(states))
	for _, state := range states {
		if state >= len(pattern) {
			continue
		}
		token := pattern[state]
		switch token.kind {
		case literalToken:
			if value == token.literal {
				next = append(next, state+1)
			}
		case anyToken:
			if value != '/' {
				next = append(next, state+1)
			}
		case starToken:
			if value != '/' {
				next = append(next, state)
			}
		case doubleStarToken:
			next = append(next, state)
		case classToken:
			if token.allowed[value] {
				next = append(next, state+1)
			}
		}
	}
	if len(next) == 0 {
		return nil
	}
	return epsilonClosure(pattern, next)
}

func productAccepts(patterns [][]globToken, states [][]int) bool {
	for index, pattern := range patterns {
		accepted := false
		for _, state := range states[index] {
			if state == len(pattern) {
				accepted = true
				break
			}
		}
		if !accepted {
			return false
		}
	}
	return true
}

func productKey(states [][]int) string {
	var builder strings.Builder
	for _, patternStates := range states {
		for _, state := range patternStates {
			builder.WriteString(strconv.Itoa(state))
			builder.WriteByte(',')
		}
		builder.WriteByte(';')
	}
	return builder.String()
}
