package schema

import (
	"fmt"
	"strings"
	"testing"
)

func TestPITRParseDefaultsAndValidatesRecoveryDays(t *testing.T) {
	s, err := Parse(`model Item { id String @id @@table(name: "items") @@pitr(enabled: true) }`)
	if err != nil {
		t.Fatal(err)
	}
	m := s.Models[0]
	if m.PITR == nil || !m.PITR.Enabled || m.PITR.RecoveryDays != 35 {
		t.Fatalf("unexpected default PITR policy: %#v", m.PITR)
	}
	for _, days := range []int32{1, 14, 35} {
		s, err := Parse(`model Item { id String @id @@table(name: "items") @@pitr(enabled: true, recoveryDays: ` + strconvI32(days) + `) }`)
		if err != nil || len(Validate(s)) != 0 {
			t.Fatalf("valid recovery window %d: schema=%#v diagnostics=%v err=%v", days, s, Validate(s), err)
		}
	}
	for _, src := range []string{
		`model Item { id String @id @@table(name: "items") @@pitr() }`,
		`model Item { id String @id @@table(name: "items") @@pitr(enabled: maybe) }`,
		`model Item { id String @id @@table(name: "items") @@pitr(enabled: true, recoveryDays: nope) }`,
		`model Item { id String @id @@table(name: "items") @@pitr(enabled: true, days: 7) }`,
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("expected parse error for %s", src)
		}
	}
}

func TestPITRValidationAndSharedTableRules(t *testing.T) {
	tests := []struct{ source, expected string }{
		{`model Item { id String @id @@table(name: "items") @@pitr(enabled: true, recoveryDays: 0) }`, "PITR recoveryDays must be between 1 and 35"},
		{`model Item { id String @id @@table(name: "items") @@pitr(enabled: true, recoveryDays: 36) }`, "PITR recoveryDays must be between 1 and 35"},
		{`model Item { id String @id @@table(name: "items") @@pitr(enabled: false, recoveryDays: 14) }`, "PITR recoveryDays is only valid when enabled is true"},
		{`model Item { id String @id @@table(name: "items") @@pitr(enabled: true) @@pitr(enabled: false) }`, "model may declare only one @@pitr"},
	}
	for _, test := range tests {
		s, err := Parse(test.source)
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticMessages(Validate(s)); !strings.Contains(got, test.expected) {
			t.Errorf("diagnostics %q do not include %q", got, test.expected)
		}
	}
	s, err := Parse(`model A { id String @id @@table(name: "shared") @@pitr(enabled: true, recoveryDays: 35) }
model B { id String @id @@table(name: "shared") @@pitr(enabled: true) }`)
	if err != nil || len(Validate(s)) != 0 {
		t.Fatalf("equivalent shared PITR policies should validate: %v", Validate(s))
	}
	s, err = Parse(`model A { id String @id @@table(name: "shared") @@pitr(enabled: true) }
model B { id String @id @@table(name: "shared") @@pitr(enabled: false) }`)
	if err != nil || !strings.Contains(diagnosticMessages(Validate(s)), "PITR declarations differ") {
		t.Fatalf("conflicting shared policies were not rejected: %v", Validate(s))
	}
}

func strconvI32(value int32) string { return fmt.Sprint(value) }
