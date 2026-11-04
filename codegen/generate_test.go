package codegen

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/liberocks/dynamox/schema"
)

func TestGeneratedQueriesOnlyExposeIndexedFields(t *testing.T) {
	src, err := os.ReadFile("../examples/blog/schema.dynamox")
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	code, err := Generate(s, "generated")
	if err != nil {
		t.Fatal(err)
	}
	text := string(code)
	if !strings.Contains(text, "QueryPostUsingByAuthor") {
		t.Fatal("missing declared GSI query method")
	}
	if strings.Contains(text, "QueryUserByName") || strings.Contains(text, "QueryPostByTitle") {
		t.Fatal("generator exposed a query for a non-indexed field")
	}
}

func TestGenerateCoversScalarAndComplexShapes(t *testing.T) {
	src := `enum Status { READY DONE }
type Payload {
  created DateTime
  labels String[]
  status Status?
}
model Entry {
  id String @id
  category Status
  required Payload @jsonString
  optional Payload? @jsonString
  instant DateTime @sortKey
  maybeInstant DateTime?
  instants DateTime[]
  maybeInstants DateTime[]?
  caption String?
  @@table(name: "entries")
}`
	s, err := schema.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate(s, "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, want := range []string{"package dynamoxgen", "type Status string", "StatusREADY", "*Payload", "*[]time.Time", "if v.Optional != nil", "if v.MaybeInstant != nil", "if v.MaybeInstants != nil"} {
		if !strings.Contains(text, want) {
			t.Errorf("generated code missing %q:\n%s", want, text)
		}
	}
	if _, err := Generate(s, "invalid-package"); err == nil {
		t.Fatal("expected formatter error for invalid package")
	}
	for _, packageName := range []string{"package", "_", "x; import \"os\""} {
		if _, err := Generate(s, packageName); err == nil {
			t.Errorf("Generate accepted unsafe package name %q", packageName)
		}
	}
	bad := &schema.Schema{}
	if _, err := Generate(bad, "x"); err == nil {
		t.Fatal("expected schema validation error")
	}
}

func TestGeneratorHelperEdgeCases(t *testing.T) {
	for _, tc := range []struct{ typ, want string }{{"String", "string"}, {"Int", "int64"}, {"Float", "float64"}, {"Boolean", "bool"}, {"DateTime", "time.Time"}, {"Bytes", "[]byte"}, {"CustomType", "CustomType"}} {
		f := &schema.Field{Type: tc.typ}
		if got := baseType(f, &schema.Schema{}); got != tc.want {
			t.Errorf("baseType(%q)=%q, want %q", tc.typ, got, tc.want)
		}
	}
	if got := baseType(nil, &schema.Schema{}); got != "string" {
		t.Fatalf("nil base type %q", got)
	}
	if got := fieldType(schema.Field{Type: "String", List: true, Optional: true}, &schema.Schema{}); got != "*[]string" {
		t.Fatal(got)
	}
	if got := valueType(schema.Field{Type: "String", List: true, Optional: true}, &schema.Schema{}); got != "[]string" {
		t.Fatal(got)
	}
	for in, want := range map[string]string{"": "X", "123-name": "X123Name", "user_id": "UserID", "user_url": "UserURL", "http_api": "HTTPAPI", "aws_json": "AWSJSON", "a.b": "AB"} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q)=%q want %q", in, got, want)
		}
	}
	if got := lowerFirst(""); got != "" {
		t.Fatal(got)
	}
	if got := lowerFirst("User"); got != "user" {
		t.Fatal(got)
	}
	if got := keyNames(schema.Model{Partition: "id"}); got != "\"id\"" {
		t.Fatal(got)
	}
	if got := keyNames(schema.Model{Partition: "id", Sort: "sort"}); got != "\"id\",\"sort\"" {
		t.Fatal(got)
	}
	if got := findRelationIndex(&schema.Model{Indexes: []schema.Index{{Name: "Other", Partition: "owner"}}}, schema.Relation{Remote: "missing"}); got.Name != "" {
		t.Fatal(got)
	}
	var b bytes.Buffer
	generateRelation(&b, schema.Model{Name: "Entry"}, schema.Relation{Target: "Missing"}, &schema.Schema{})
	if b.Len() != 0 {
		t.Fatal("missing relation target should be ignored")
	}
}

func TestFormatGeneratedReturnsFormattingErrors(t *testing.T) {
	if _, err := formatGenerated([]byte("package")); err == nil || !strings.Contains(err.Error(), "format generated Go") {
		t.Fatalf("formatGenerated did not report invalid Go source: %v", err)
	}
}

func TestGenerateEmitsConfiguredIDStrategies(t *testing.T) {
	src := `model V1 { id String @id @default(uuidv1) @@table(name: "v1") }
model V4 { id String @id @default(uuidv4) @@table(name: "v4") }
model V7 { id String @id @default(uuidv7) @@table(name: "v7") }
model Object { id String @id @default(objectid) @@table(name: "objects") }
model Serial { id Int @id @default(serial) @@table(name: "serials") }`
	s, err := schema.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate(s, "idgen")
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, want := range []string{`c.idGenerator("uuidv1")`, `c.idGenerator("uuidv4")`, `c.idGenerator("uuidv7")`, `c.idGenerator("objectid")`, `runtime.NextSerialWithTable(ctx, c.db(), c.sequenceTable, "serials")`, `func NewClientWithIDGenerator`} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q", want)
		}
	}
}

func TestGenerateHandlesRequiredAndOptionalDocumentJSON(t *testing.T) {
	s, err := schema.Parse(`type Child { value String }
type Metadata { required Json optional Json? child Child? @document }
model Record { id String @id document Metadata @document @@table(name: "records") }`)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate(s, "documents")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"var zeroRequired runtime.JSON", "runtime.MarshalJSON(v.Required)", "runtime.MarshalJSON(*v.Optional)", `dynamodbav:"child,omitempty"`} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("generated document codec missing %q", want)
		}
	}
}
