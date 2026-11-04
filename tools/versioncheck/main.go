package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type semVersion [3]string

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "version check:", err)
		os.Exit(1)
	}
}

func run() error {
	current, err := os.ReadFile("VERSION")
	if err != nil {
		return fmt.Errorf("read VERSION: %w", err)
	}
	master, err := exec.Command("git", "show", "refs/remotes/origin/master:VERSION").Output()
	if err != nil {
		return fmt.Errorf("read VERSION from master: %w", err)
	}
	if err := checkVersionBump(string(current), string(master)); err != nil {
		return err
	}
	fmt.Printf("Dynamox version %s is greater than master version %s\n", strings.TrimSpace(string(current)), strings.TrimSpace(string(master)))
	return nil
}

func checkVersionBump(candidateText, masterText string) error {
	candidate, err := parseVersion(candidateText)
	if err != nil {
		return fmt.Errorf("invalid VERSION: %w", err)
	}
	master, err := parseVersion(masterText)
	if err != nil {
		return fmt.Errorf("invalid master VERSION: %w", err)
	}
	if compareVersions(candidate, master) <= 0 {
		return fmt.Errorf("VERSION %s must be greater than master version %s", formatVersion(candidate), formatVersion(master))
	}
	return nil
}

func parseVersion(text string) (semVersion, error) {
	var version semVersion
	parts := strings.Split(strings.TrimSpace(text), ".")
	if len(parts) != len(version) {
		return version, fmt.Errorf("expected MAJOR.MINOR.PATCH")
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version, fmt.Errorf("invalid numeric component %q", part)
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return version, fmt.Errorf("invalid numeric component %q", part)
			}
		}
		version[i] = part
	}
	return version, nil
}

func compareVersions(left, right semVersion) int {
	for i := range left {
		if len(left[i]) < len(right[i]) {
			return -1
		}
		if len(left[i]) > len(right[i]) {
			return 1
		}
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func formatVersion(version semVersion) string {
	return strings.Join(version[:], ".")
}
