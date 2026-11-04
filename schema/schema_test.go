package schema

import (
	"strings"
	"testing"
)

const fixture = `// account schema
model Account {
  id String @id // stable identifier
  posts Post[] @relation(fields: [id], references: [accountId], index: "ByAccount")
  @@table(name: "accounts")
}

model Post {
  id String @id
  accountId String
  body String
  @@table(name: "posts")
  @@index(name: "ByAccount", partition: accountId, type: GSI)
}`

func TestFormatIsStableAndPreservesComments(t *testing.T) {
	first, err := Format(fixture)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Format(first)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("format is not idempotent:\n%s\nthen:\n%s", first, second)
	}
	if !strings.Contains(first, "// account schema") || !strings.Contains(first, "id String @id // stable identifier") {
		t.Fatalf("comments were lost:\n%s", first)
	}
	if _, err := Parse(first); err != nil {
		t.Fatalf("formatted schema does not parse: %v", err)
	}
}

func TestFormatSeparatesTopLevelBlocksAndKeepsCommentsWithNextBlock(t *testing.T) {
	src := `model First {
  id String @id
  @@table(name: "first")
}
// second block
model Second {
  id String @id
  @@table(name: "second")
}
enum State { READY DONE }
model Third {
  id String @id
  @@table(name: "third")
}`
	formatted, err := Format(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"}\n\n// second block\nmodel Second",
		"}\n\nenum State { READY DONE }\n\nmodel Third",
	} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted schema should contain %q:\n%s", want, formatted)
		}
	}
	second, err := Format(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if second != formatted {
		t.Fatalf("block spacing is not idempotent:\n%s\nthen:\n%s", formatted, second)
	}
}

func TestBlankBeforeTrailingCommentsWithoutPreviousBlock(t *testing.T) {
	lines := []string{"// standalone comment"}
	got := blankBeforeTrailingComments(lines)
	if len(got) != 1 || got[0] != lines[0] {
		t.Fatalf("standalone comment changed: %q", got)
	}
}

func TestValidateRelationRequiresIndexedReadPath(t *testing.T) {
	src := strings.Replace(fixture, `index: "ByAccount"`, `index: "Missing"`, 1)
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	ds := Validate(s)
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "target index") {
		t.Fatalf("expected index diagnostic, got %#v", ds)
	}
	if ds[0].Position.Line != 4 {
		t.Fatalf("expected relation location on line 4, got %#v", ds[0].Position)
	}
}

func TestValidateRejectsUnknownFieldsAndDuplicateKeys(t *testing.T) {
	src := `model Bad {
  id String @id
  other String @partitionKey
  @@table(name: "bad")
}`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	ds := Validate(s)
	found := false
	for _, d := range ds {
		if strings.Contains(d.Message, "only one partition key") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate key diagnostic, got %#v", ds)
	}
}

func TestValidateRejectsGoNameCollisionsAndByValueCycles(t *testing.T) {
	src := `type First_Name {
  child SecondName
}
type FirstName {
  value String
}
type SecondName {
  parent First_Name
}
model User {
  id String @id
  @@table(name: "users")
}`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	ds := Validate(s)
	var collision, cycle bool
	for _, d := range ds {
		collision = collision || strings.Contains(d.Message, "same Go name") || strings.Contains(d.Message, "generated Go name")
		cycle = cycle || strings.Contains(d.Message, "recursive JSON types")
	}
	if !collision || !cycle {
		t.Fatalf("expected generated-name collision and type-cycle diagnostics, got %#v", ds)
	}
}
