package schema

import (
	"strings"
	"testing"
)

func TestParseTTLAndProvisionedCapacity(t *testing.T) {
	src := `model Item {
  id String @id
  sort String @sortKey
  expiresAt Int?
  group String
  localSort String
  @@table(name: "items", read: 5, write: 2)
  @@index(name: "ByGroup", partition: group, type: GSI, read: 3, write: 1)
  @@index(name: "ByLocal", sort: localSort, type: LSI)
  @@ttl(field: expiresAt)
}`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	m := s.Models[0]
	if m.TTL != "expiresAt" || m.TTLCount != 1 || m.ReadCapacityUnits == nil || *m.ReadCapacityUnits != 5 || m.WriteCapacityUnits == nil || *m.WriteCapacityUnits != 2 {
		t.Fatalf("model provisioning settings: %#v", m)
	}
	if len(m.Indexes) != 2 || m.Indexes[0].ReadCapacityUnits == nil || *m.Indexes[0].ReadCapacityUnits != 3 || m.Indexes[0].WriteCapacityUnits == nil || *m.Indexes[0].WriteCapacityUnits != 1 {
		t.Fatalf("index provisioning settings: %#v", m.Indexes)
	}
	if ds := Validate(s); len(ds) > 0 {
		t.Fatalf("valid schema diagnostics: %v", ds)
	}
	formatted, err := Format(src)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := Parse(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if ds := Validate(reparsed); len(ds) > 0 {
		t.Fatalf("formatted schema diagnostics: %v\n%s", ds, formatted)
	}
}

func TestParseRejectsNonIntegerCapacityAndUnknownTTLArguments(t *testing.T) {
	for _, src := range []string{
		`model Item { id String @id @@table(name: "items", read: "2", write: 1) }`,
		`model Item { id String @id @@table(name: "items", write: nope) }`,
		`model Item { id String @id @@index(name: "ById", partition: id, read: nope) }`,
		`model Item { id String @id @@index(name: "ById", partition: id, write: nope) }`,
		`model Item { id String @id @@ttl(name: expiresAt) }`,
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) should fail", src)
		}
	}
}

func TestValidateTTLAndCapacityRules(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "partial table capacity",
			src:  `model Item { id String @id @@table(name: "items", read: 2) }`,
			want: []string{"table read and write capacity must be specified together"},
		},
		{
			name: "nonpositive table capacity",
			src:  `model Item { id String @id @@table(name: "items", read: 0, write: -1) }`,
			want: []string{"table read capacity must be positive", "table write capacity must be positive"},
		},
		{
			name: "provisioned GSI requirements",
			src: `model Item {
 id String @id
 group String
 @@table(name: "items", read: 2, write: 2)
 @@index(name: "ByGroup", partition: group, type: GSI, read: 1)
}`,
			want: []string{"index ByGroup read and write capacity must be specified together"},
		},
		{
			name: "GSI capacity on on-demand table",
			src: `model Item {
 id String @id
 group String
 @@table(name: "items")
 @@index(name: "ByGroup", partition: group, type: GSI, read: 1, write: 1)
}`,
			want: []string{"GSI capacity requires table read and write capacity"},
		},
		{
			name: "missing provisioned GSI capacity",
			src: `model Item {
 id String @id
 group String
 @@table(name: "items", read: 2, write: 2)
 @@index(name: "ByGroup", partition: group, type: GSI)
}`,
			want: []string{"GSI ByGroup requires read and write capacity"},
		},
		{
			name: "LSI capacity is inherited",
			src: `model Item {
 id String @id
 sort String @sortKey
 other String
 @@table(name: "items", read: 2, write: 2)
 @@index(name: "ByLocal", sort: other, type: LSI, read: 1, write: 1)
}`,
			want: []string{"LSI shares table throughput and cannot specify capacity"},
		},
		{
			name: "TTL target rules",
			src: `model Item {
 id String @id
 expiresAt String
 @@table(name: "items")
 @@ttl(field: expiresAt)
 @@ttl(field: missing)
}`,
			want: []string{"model may declare only one @@ttl", "TTL references unknown field missing"},
		},
		{
			name: "unknown and missing TTL target",
			src: `model Item {
 id String @id
 @@table(name: "items")
 @@ttl(field: missing)
}`,
			want: []string{"TTL references unknown field missing"},
		},
		{
			name: "missing TTL field argument",
			src:  `model Item { id String @id @@table(name: "items") @@ttl() }`,
			want: []string{"@@ttl requires field: fieldName"},
		},
		{
			name: "TTL list target",
			src: `model Item {
 id String @id
 expiresAt Int[]
 @@table(name: "items")
 @@ttl(field: expiresAt)
}`,
			want: []string{"TTL field must be a scalar Int field"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			diagnostics := diagnosticMessages(Validate(s))
			for _, want := range tt.want {
				if !strings.Contains(diagnostics, want) {
					t.Errorf("missing diagnostic %q in:\n%s", want, diagnostics)
				}
			}
		})
	}
}
