package schema

import (
	"strings"
	"testing"
)

func testAttr(name string, args map[string]string) Attribute {
	return Attribute{Name: name, Args: args, Pos: Position{Line: 2, Column: 3}}
}
func testField(name, typ string) Field {
	return Field{Name: name, Type: typ, Pos: Position{Line: 3, Column: 1}}
}
func testModel(name, table, partition string, fields ...Field) Model {
	return Model{Name: name, Table: table, Partition: partition, Fields: fields, Pos: Position{Line: 1, Column: 1}}
}
func diagnosticMessages(ds []Diagnostic) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.Message
	}
	return strings.Join(parts, "\n")
}
func hasDiagnostic(ds []Diagnostic, fragment string) bool {
	return strings.Contains(diagnosticMessages(ds), fragment)
}

func TestValidateDeclarationsNamesAndEmptySchema(t *testing.T) {
	if ds := Validate(&Schema{}); len(ds) != 1 || !strings.Contains(ds[0].Message, "at least one model") {
		t.Fatalf("empty schema: %v", ds)
	}
	first := testModel("Client", "", "missing")
	second := testModel("Client", "shared", "id", testField("id", "String"), testField("id", "String"), testField("foo_bar", "String"), testField("fooBar", "String"))
	second.Fields[0].Attributes = []Attribute{testAttr("id", nil)}
	second.Fields[0].Pos = Position{Line: 4, Column: 2}
	second.Fields[1].Pos = Position{Line: 5, Column: 2}
	second.Fields[2].Pos = Position{Line: 6, Column: 2}
	second.Fields[3].Pos = Position{Line: 7, Column: 2}
	second.Indexes = []Index{{Name: "first", Partition: "id", Kind: "TABLE", Pos: Position{Line: 8, Column: 1}}, {Name: "second", Partition: "id", Kind: "TABLE", Pos: Position{Line: 9, Column: 1}}}
	s := &Schema{Models: []Model{first, second, testModel("Thing", "shared", "id", testField("id", "String"))}, Types: []Composite{
		{Name: "Client", Pos: Position{Line: 10, Column: 1}},
		{Name: "Payload", Pos: Position{Line: 11, Column: 1}},
		{Name: "Payload", Pos: Position{Line: 12, Column: 1}},
		{Name: "Foo_Bar", Pos: Position{Line: 13, Column: 1}, Fields: []Field{{Name: "x_y", Type: "String", Pos: Position{Line: 14, Column: 1}}, {Name: "xY", Type: "String", Pos: Position{Line: 15, Column: 1}}}},
	}, Enums: []Enum{
		{Name: "Client", Pos: Position{Line: 16, Column: 1}, Values: []string{"A"}},
		{Name: "Payload", Pos: Position{Line: 17, Column: 1}, Values: []string{"A"}},
		{Name: "Status", Pos: Position{Line: 18, Column: 1}, Values: []string{"READY", "READY"}},
		{Name: "Status", Pos: Position{Line: 20, Column: 1}, Values: []string{"DONE", "DONE"}},
		{Name: "Empty", Pos: Position{Line: 19, Column: 1}},
	}}
	ds := Validate(s)
	joined := diagnosticMessages(ds)
	for _, want := range []string{"duplicate model Client", "type name conflicts with model Client", "enum name conflicts with model Client", "type name conflicts with enum Payload", "duplicate type Payload", "duplicate enum Status", "duplicate enum value DONE", "enum must contain at least one value", "generated Go name", "model must declare @@table", "table \"shared\" is already assigned", "fields \"fooBar\" and \"foo_bar\" map to the same Go name", "indexes generate duplicate method", "duplicate field id", "fields \"xY\" and \"x_y\" map to the same Go name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in diagnostics:\n%s", want, joined)
		}
	}
	if got := positionFieldName(second, Position{Line: 99, Column: 1}); got != "?" {
		t.Fatalf("missing field position = %q", got)
	}
	if got := positionFieldNameComposite(Composite{}, Position{}); got != "?" {
		t.Fatalf("missing composite field position = %q", got)
	}
	if got := goIdentifier("5-item"); got != "X5Item" {
		t.Fatal(got)
	}
	if got := goIdentifier("http_json_api_aws_url_id"); got != "HTTPJSONAPIAWSURLID" {
		t.Fatal(got)
	}
}

func TestValidateFieldsIndexesAndKeys(t *testing.T) {
	f := []Field{
		{Name: "id", Type: "String", Optional: true, Attributes: []Attribute{testAttr("id", nil)}, Pos: Position{Line: 2, Column: 1}},
		{Name: "another", Type: "String", Attributes: []Attribute{testAttr("partitionKey", nil)}, Pos: Position{Line: 3, Column: 1}},
		{Name: "sort", Type: "Boolean", Optional: true, Attributes: []Attribute{testAttr("sortKey", nil)}, Pos: Position{Line: 4, Column: 1}},
		{Name: "sort2", Type: "Int", Attributes: []Attribute{testAttr("sortKey", nil)}, Pos: Position{Line: 5, Column: 1}},
		{Name: "dup", Type: "String", Pos: Position{Line: 6, Column: 1}},
		{Name: "dup", Type: "String", Pos: Position{Line: 7, Column: 1}},
		{Name: "list", Type: "String", List: true, Pos: Position{Line: 8, Column: 1}},
		{Name: "optional", Type: "Int", Optional: true, Pos: Position{Line: 9, Column: 1}},
		{Name: "composite", Type: "Payload", Pos: Position{Line: 10, Column: 1}},
		{Name: "scalarJson", Type: "String", Attributes: []Attribute{testAttr("jsonString", map[string]string{"bad": "x"})}, Pos: Position{Line: 11, Column: 1}},
		{Name: "compositeAttr", Type: "Payload", Attributes: []Attribute{testAttr("unknown", nil)}, Pos: Position{Line: 12, Column: 1}},
		{Name: "unknown", Type: "Unknown", Pos: Position{Line: 13, Column: 1}},
		{Name: "emptyType", Type: "", Pos: Position{Line: 14, Column: 1}},
		{Name: "relScalar", Type: "String", Attributes: []Attribute{testAttr("relation", map[string]string{"bad": "x"}), testAttr("wrong", nil)}, Pos: Position{Line: 15, Column: 1}},
		{Name: "relationModel", Type: "Target", Attributes: []Attribute{testAttr("relation", map[string]string{"fields": "id", "references": "id"})}, Pos: Position{Line: 16, Column: 1}},
	}
	m := testModel("Bad", "", "id", f...)
	m.Sort = "sort"
	m.Indexes = []Index{
		{},
		{Name: "weird", Kind: "NOPE", Partition: "missing", Sort: "missingSort"},
		{Name: "local1", Kind: "LSI"},
		{Name: "local2", Kind: "LSI", Partition: "another", Sort: "sort"},
		{Name: "duplicate", Kind: "GSI", Partition: "optional", Sort: "list"},
		{Name: "duplicate", Kind: "GSI", Partition: "composite", Sort: "unknown"},
		{Name: "byIdA", Kind: "GSI", Partition: "id"},
		{Name: "byIdB", Kind: "GSI", Partition: "id"},
	}
	noSort := testModel("NoSort", "nosort", "id", Field{Name: "id", Type: "KeyEnum", Attributes: []Attribute{testAttr("id", nil)}})
	noSort.Indexes = []Index{{Name: "needsSort", Kind: "LSI"}}
	noPartition := testModel("NoPartition", "nopartition", "", testField("value", "String"))
	unknownKey := testModel("UnknownKey", "unknownkey", "missing", testField("id", "String"))
	unknownKey.Sort = "missingSort"
	s := &Schema{Models: []Model{m, noSort, noPartition, unknownKey}, Types: []Composite{{Name: "Payload"}}, Enums: []Enum{{Name: "KeyEnum", Values: []string{"one"}}}}
	ds := Validate(s)
	joined := diagnosticMessages(ds)
	for _, want := range []string{"model must declare @@table", "model must declare one @partitionKey", "only one partition key", "only one sort key", "partition key references unknown field missing", "sort key references unknown field missingSort", "partition key must be a required", "sort key must be a required", "duplicate field dup", "partition key cannot be optional", "sort key cannot be optional", "@jsonString requires a named type or enum", "composite field composite must use @jsonString", "unknown field type Unknown", "field type is required", "relation target must be a model", "relation fields cannot have @wrong", "unknown @relation argument bad", "unknown field attribute @unknown", "index name is required", "GSI requires a partition field", "index type must be GSI or LSI", "LSI requires a sort field", "LSI requires a table sort key", "LSI partition must match", "index references unknown partition field missing", "index references unknown sort field missingSort", "index partition field must be a required", "index sort field must be a required", "duplicate index name duplicate"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in diagnostics:\n%s", want, joined)
		}
	}
	for _, tc := range []struct {
		f    *Field
		s    *Schema
		want bool
	}{{nil, s, false}, {&Field{Type: "Boolean"}, s, false}, {&Field{Type: "String", Optional: true}, s, false}, {&Field{Type: "String", List: true}, s, false}, {&Field{Type: "String"}, s, true}, {&Field{Type: "KeyEnum"}, s, true}, {&Field{Type: "Payload"}, s, false}} {
		if got := validKeyField(tc.f, tc.s); got != tc.want {
			t.Errorf("validKeyField(%#v)=%v want %v", tc.f, got, tc.want)
		}
	}
}

func TestValidateRelationsCompositeFieldsAndCycles(t *testing.T) {
	target := testModel("Target", "targets", "id", testField("id", "String"), testField("sort", "String"), testField("name", "String"), testField("ownerId", "String"))
	target.Sort = "sort"
	target.Fields[0].Attributes = []Attribute{testAttr("id", nil)}
	target.Indexes = []Index{{Name: "ByOwner", Partition: "ownerId", Kind: "GSI"}}
	source := testModel("Source", "sources", "id", testField("id", "String"), testField("fk", "String"), Field{Name: "optionalFk", Type: "String", Optional: true}, testField("numberFk", "Int"))
	source.Fields[1].Pos = Position{Line: 40, Column: 1}
	source.Fields[0].Attributes = []Attribute{testAttr("id", nil)}
	source.Relations = []Relation{
		{FieldName: "unknown", Target: "Missing", Pos: Position{Line: 20, Column: 1}},
		{FieldName: "empty", Target: "Target", Pos: Position{Line: 21, Column: 1}},
		{FieldName: "manyBad", Target: "Target", Local: "missing,other", Remote: "nope,none", Many: true, Optional: true, Index: "missing", Pos: Position{Line: 22, Column: 1}},
		{FieldName: "optionalMismatch", Target: "Target", Local: "fk", Remote: "id", Optional: true, Pos: Position{Line: 23, Column: 1}},
		{FieldName: "wrongRemote", Target: "Target", Local: "fk", Remote: "name", Pos: Position{Line: 24, Column: 1}},
		{FieldName: "wrongType", Target: "Target", Local: "numberFk", Remote: "id", Pos: Position{Line: 25, Column: 1}},
		{FieldName: "manyOk", Target: "Target", Local: "fk", Remote: "ownerId", Many: true, Pos: Position{Line: 26, Column: 1}},
		{FieldName: "manyNoIndex", Target: "Target", Local: "fk", Remote: "ownerId", Many: true, Index: "Wrong", Pos: Position{Line: 27, Column: 1}},
		{FieldName: "optionalOk", Target: "Target", Local: "optionalFk", Remote: "id", Optional: true, Pos: Position{Line: 28, Column: 1}},
	}
	types := []Composite{
		{Name: "A", Fields: []Field{{Name: "b", Type: "B", Pos: Position{Line: 31, Column: 1}}}},
		{Name: "B", Fields: []Field{{Name: "a", Type: "A", Pos: Position{Line: 32, Column: 1}}}},
		{Name: "RecursiveAllowed", Fields: []Field{{Name: "next", Type: "RecursiveAllowed", Optional: true}, {Name: "children", Type: "RecursiveAllowed", List: true}, {Name: "state", Type: "State"}}},
		{Name: "BadFields", Fields: []Field{{Name: "dup", Type: "String"}, {Name: "dup", Type: "NotKnown"}, {Name: "attr", Type: "Int", Attributes: []Attribute{testAttr("id", nil)}}}},
	}
	s := &Schema{Models: []Model{target, source}, Types: types, Enums: []Enum{{Name: "State", Values: []string{"ON"}}}}
	ds := Validate(s)
	joined := diagnosticMessages(ds)
	for _, want := range []string{"unknown relation model Missing", "@relation requires fields", "to-many relation fields cannot be optional", "v1 relations support one local and one referenced field", "relation fields references unknown local field", "relation references unknown target field", "to-one relation optionality must match", "relation local and referenced fields must have the same type", "needs a target index partitioned by ownerId", "to-one relation Source.wrongRemote must reference target partition key id", "to-one relation to a composite primary key is not supported", "duplicate field dup", "unknown JSON field type NotKnown", "attributes are not supported on JSON type fields", "recursive JSON types must use an optional or list field"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in diagnostics:\n%s", want, joined)
		}
	}
	if got := positionFieldName(source, source.Fields[1].Pos); got != "fk" {
		t.Fatal(got)
	}
	if got := positionFieldNameComposite(types[3], types[3].Fields[0].Pos); got != "dup" {
		t.Fatal(got)
	}
}
