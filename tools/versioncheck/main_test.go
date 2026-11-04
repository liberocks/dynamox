package main

import (
	"strings"
	"testing"
)

func TestCheckVersionBumpAcceptsPatchMinorAndMajorBumps(t *testing.T) {
	for _, candidate := range []string{"0.1.1", "0.2.0", "1.0.0", "0.10.0"} {
		if err := checkVersionBump(candidate, "0.1.0"); err != nil {
			t.Errorf("checkVersionBump(%q) rejected a newer version: %v", candidate, err)
		}
	}
}

func TestCheckVersionBumpRejectsEqualLowerAndInvalidVersions(t *testing.T) {
	for _, candidate := range []string{"0.1.0", "0.0.9", "v1.0.0", "1.0", "1.0.0-rc.1", "01.0.0", "1.00.0", "1.0.00", "1.x.0", "1.0.0\n2.0.0"} {
		if err := checkVersionBump(candidate, "0.1.0"); err == nil {
			t.Errorf("checkVersionBump(%q) accepted an invalid or non-incremented version", candidate)
		}
	}
	if err := checkVersionBump("1.0.0", "bad"); err == nil || !strings.Contains(err.Error(), "master VERSION") {
		t.Fatalf("invalid master baseline was not rejected: %v", err)
	}
}

func TestParseVersionAndCompareLargeComponents(t *testing.T) {
	parsed, err := parseVersion(" 999999999999999999999999.2.3\n")
	if err != nil || formatVersion(parsed) != "999999999999999999999999.2.3" {
		t.Fatalf("large semantic version did not round-trip: %q %v", formatVersion(parsed), err)
	}
	left, _ := parseVersion("0.10.0")
	right, _ := parseVersion("0.9.99")
	if compareVersions(left, right) <= 0 {
		t.Fatalf("numeric component order is wrong: %s <= %s", formatVersion(left), formatVersion(right))
	}
}
