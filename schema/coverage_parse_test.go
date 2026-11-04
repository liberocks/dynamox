package schema

import (
	"strings"
	"testing"
)

func TestParserCoversBlocksArgumentsAndAttributes(t *testing.T) {
	src := `// type docs
type RecordMeta {
  at DateTime
  labels String[]
  current Status?
}
enum Status { READY DONE }
model Record {
  id String @id
  sort DateTime @sortKey
  owner String?
  data RecordMeta @jsonString
  children Record[] @relation(fields: [id], references: [id], index: "ByOwner")
  @@table("records")
  @@index(name: "ByOwner", partition: owner, sort: sort, type: GSI)
}`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Models) != 1 || len(s.Types) != 1 || len(s.Enums) != 1 {
		t.Fatalf("parsed blocks: %#v", s)
	}
	if s.Models[0].Table != "records" || s.Models[0].Sort != "sort" || len(s.Models[0].Relations) != 1 || s.Models[0].Relations[0].Index != "ByOwner" {
		t.Fatalf("parsed model: %#v", s.Models[0])
	}
	if !s.Types[0].Fields[1].List || !s.Types[0].Fields[2].Optional {
		t.Fatalf("composite modifiers: %#v", s.Types[0].Fields)
	}
	if _, err = Parse(`model A { x String @tag(key: [one, two], other: "x") @@table(name: "a") }`); err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(`type A { field String @id }`); err != nil {
		t.Fatal(err)
	}
	if _, err = Format("}\n{\n  field: value?\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = Format("bad # token"); err == nil {
		t.Fatal("format should report lexer error")
	}
	if _, err = Format("unknown declaration"); err != nil {
		t.Fatalf("format should handle an unknown top-level identifier: %v", err)
	}
}

func TestParserReportsMalformedTokensAndBlocks(t *testing.T) {
	for _, src := range []string{
		`model A { x String # }`,
		"model A { x String @id\n",
		`model A { x String @id`,
		`model "A" {}`,
		`model {}`,
		`model A [`,
		`other A {}`,
		`model A { field }`,
		`model A { @ }`,
		`model A { field String[ }`,
		`model A { field String @ }`,
		`model A { @@(name: "a") }`,
		`model A { @@table(name: "a" }`,
		`model A { @@table(name: "a"`,
		`model A { @@table name }`,
		`model A { @@table(name: "a" other: "b") }`,
		`model A { @@table(other: "a") }`,
		`model A { @@index(other: "a") }`,
		`model A { @@bad() }`,
		`type A { field }`,
		`type A { field String[ }`,
		`type A { field String @ }`,
		`enum A { "bad" }`,
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", src)
		}
	}
	for _, src := range []string{"model A { x String @id\n y String", "type A { x String", "enum A { VALUE"} {
		if _, err := Parse(src); err == nil {
			t.Errorf("unclosed block accepted: %q", src)
		}
	}
}

func TestLexerAndParserInternalErrorPaths(t *testing.T) {
	for _, src := range []string{"#", `"unterminated`, "\"line\nbreak\""} {
		if _, err := lex(src); err == nil {
			t.Errorf("lex(%q) unexpectedly succeeded", src)
		}
	}
	if _, err := lex(`model A { x String @tag(text: "a\\\"b") }`); err != nil {
		t.Fatal(err)
	}
	p := parser{toks: []token{{text: "x", kind: ident, pos: Position{Line: 1, Column: 1}}}}
	if p.peek().text != "x" {
		t.Fatal("peek token")
	}
	p.i = 2
	if p.peek().kind != eof {
		t.Fatal("peek past end")
	}
	p.i = 0
	if _, err := p.want(stringLit); err == nil {
		t.Fatal("want should reject token kind")
	}
	p.i = 0
	if _, err := p.wantText("expected"); err == nil {
		t.Fatal("wantText should reject token text")
	}
	argParser := parser{toks: []token{{text: "", kind: eof, pos: Position{Line: 1, Column: 1}}}}
	if _, err := argParser.parseArgs(); err == nil {
		t.Fatal("parseArgs should reject EOF before closing parenthesis")
	}
	if !strings.Contains(unquote(`"quoted"`), "quoted") {
		t.Fatal("unquote")
	}
}

func TestASTLookupAndDiagnosticFormatting(t *testing.T) {
	s := &Schema{Models: []Model{{Name: "First"}, {Name: "Second"}}}
	if s.Model("First") == nil || s.Model("missing") != nil {
		t.Fatal("model lookup")
	}
	m := Model{Fields: []Field{{Name: "id"}, {Name: "name"}}}
	if m.Field("name") == nil || m.Field("missing") != nil {
		t.Fatal("field lookup")
	}
	d := Diagnostic{Position: Position{Line: 4, Column: 8}, Message: "bad"}
	if d.Error() != "4:8: bad" {
		t.Fatal(d.Error())
	}
}
