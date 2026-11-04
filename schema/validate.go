package schema

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

var scalarTypes = map[string]bool{"String": true, "Int": true, "Float": true, "Boolean": true, "DateTime": true, "Bytes": true}

func Validate(s *Schema) []Diagnostic {
	var ds []Diagnostic
	add := func(p Position, msg string) { ds = append(ds, Diagnostic{p, msg}) }
	if len(s.Models) == 0 {
		add(Position{1, 1}, "schema must declare at least one model")
	}
	models := map[string]*Model{}
	tables := map[string]Position{}
	types := map[string]Composite{}
	enums := map[string]Enum{}
	for i := range s.Models {
		m := &s.Models[i]
		if _, ok := models[m.Name]; ok {
			add(m.Pos, "duplicate model "+m.Name)
		}
		models[m.Name] = m
	}
	for _, t := range s.Types {
		if _, ok := types[t.Name]; ok {
			add(t.Pos, "duplicate type "+t.Name)
		}
		types[t.Name] = t
	}
	for _, e := range s.Enums {
		if _, ok := enums[e.Name]; ok {
			add(e.Pos, "duplicate enum "+e.Name)
		}
		enums[e.Name] = e
	}
	for name := range types {
		if _, ok := models[name]; ok {
			add(types[name].Pos, "type name conflicts with model "+name)
		}
		if _, ok := enums[name]; ok {
			add(types[name].Pos, "type name conflicts with enum "+name)
		}
	}
	for name, e := range enums {
		if _, ok := models[name]; ok {
			add(e.Pos, "enum name conflicts with model "+name)
		}
		seen := map[string]bool{}
		for _, v := range e.Values {
			if seen[v] {
				add(e.Pos, "duplicate enum value "+v)
			}
			seen[v] = true
		}
		if len(e.Values) == 0 {
			add(e.Pos, "enum must contain at least one value")
		}
	}
	generated := map[string]Position{"Client": {Line: 1, Column: 1}, "NewClient": {Line: 1, Column: 1}}
	generatedMethods := map[string]Position{}
	reserve := func(name string, pos Position) {
		if previous, ok := generated[name]; ok {
			add(pos, fmt.Sprintf("generated Go name %q conflicts with declaration at %d:%d", name, previous.Line, previous.Column))
		} else {
			generated[name] = pos
		}
	}
	for _, m := range s.Models {
		n := goIdentifier(m.Name)
		reserve(n, m.Pos)
		reserve(n+"Key", m.Pos)
		reserve(n+"Update", m.Pos)
		reserve("key"+n, m.Pos)
		reserve("encode"+n, m.Pos)
		reserve("decode"+n, m.Pos)
		fields := map[string]Position{}
		for _, f := range m.Fields {
			g := goIdentifier(f.Name)
			if previous, ok := fields[g]; ok {
				add(f.Pos, fmt.Sprintf("fields %q and %q map to the same Go name %q", f.Name, positionFieldName(m, previous), g))
			} else {
				fields[g] = f.Pos
			}
		}
		for _, ix := range m.Indexes {
			method := "Query" + n + "By" + goIdentifier(ix.Partition)
			if ix.Kind != "" && ix.Kind != "TABLE" {
				method = "Query" + n + "Using" + goIdentifier(ix.Name)
			}
			methodKey := n + ":" + method
			if previous, ok := generatedMethods[methodKey]; ok {
				add(ix.Pos, fmt.Sprintf("indexes generate duplicate method %q (first at %d:%d)", method, previous.Line, previous.Column))
			} else {
				generatedMethods[methodKey] = ix.Pos
			}
			if ix.Sort != "" {
				reserve(n+goIdentifier(ix.Name)+"Sort", ix.Pos)
			}
		}
		for _, r := range m.Relations {
			reserve(n+"With"+goIdentifier(r.FieldName), r.Pos)
		}
	}
	for _, typ := range s.Types {
		reserve(goIdentifier(typ.Name), typ.Pos)
		fields := map[string]Position{}
		for _, f := range typ.Fields {
			g := goIdentifier(f.Name)
			if previous, ok := fields[g]; ok {
				add(f.Pos, fmt.Sprintf("fields %q and %q map to the same Go name %q", f.Name, positionFieldNameComposite(typ, previous), g))
			} else {
				fields[g] = f.Pos
			}
		}
	}
	for _, e := range s.Enums {
		reserve(goIdentifier(e.Name), e.Pos)
		for _, v := range e.Values {
			reserve(goIdentifier(e.Name)+goIdentifier(v), e.Pos)
		}
	}
	usesSerial := false
	for i := range s.Models {
		if f := s.Models[i].Field(s.Models[i].Partition); f != nil && f.Default == "serial" {
			usesSerial = true
		}
	}
	for _, m := range s.Models {
		if strings.TrimSpace(m.Table) == "" {
			add(m.Pos, "model must declare @@table(name: \"...\")")
		} else if usesSerial && m.Table == "dynamox_sequences" {
			add(m.Pos, "table name dynamox_sequences is reserved for serial ID generation")
		} else if previous, ok := tables[m.Table]; ok {
			add(m.Pos, fmt.Sprintf("table %q is already assigned at %d:%d", m.Table, previous.Line, previous.Column))
		} else {
			tables[m.Table] = m.Pos
		}
		keyCount := 0
		sortCount := 0
		for _, f := range m.Fields {
			if hasAttr(f, "partitionKey") || hasAttr(f, "id") {
				keyCount++
			}
			if hasAttr(f, "sortKey") {
				sortCount++
			}
		}
		if keyCount > 1 {
			add(m.Pos, "model may declare only one partition key")
		}
		if sortCount > 1 {
			add(m.Pos, "model may declare only one sort key")
		}
		if m.Partition == "" {
			add(m.Pos, "model must declare one @partitionKey (or @id) field")
		}
		if m.Partition != "" {
			if m.Field(m.Partition) == nil {
				add(m.Pos, "partition key references unknown field "+m.Partition)
			} else if !validKeyField(m.Field(m.Partition), s) {
				add(m.Field(m.Partition).Pos, "partition key must be a required String, Int, Float, DateTime, or Bytes field")
			}
		}
		if m.Sort != "" {
			if m.Field(m.Sort) == nil {
				add(m.Pos, "sort key references unknown field "+m.Sort)
			} else if !validKeyField(m.Field(m.Sort), s) {
				add(m.Field(m.Sort).Pos, "sort key must be a required String, Int, Float, DateTime, or Bytes field")
			}
		}
		seenF := map[string]bool{}
		for _, f := range m.Fields {
			defaultCount := 0
			if seenF[f.Name] {
				add(f.Pos, "duplicate field "+f.Name)
			}
			seenF[f.Name] = true
			if f.Name == m.Partition && f.Optional {
				add(f.Pos, "partition key cannot be optional")
			}
			if f.Name == m.Sort && f.Optional {
				add(f.Pos, "sort key cannot be optional")
			}
			if f.Type == "" {
				add(f.Pos, "field type is required")
			}
			isRel := hasAttr(f, "relation")
			if isRel {
				if scalarTypes[f.Type] {
					add(f.Pos, "relation target must be a model")
				}
				for _, a := range f.Attributes {
					if a.Name != "relation" {
						add(a.Pos, "relation fields cannot have @"+a.Name)
					}
					for arg := range a.Args {
						if arg != "fields" && arg != "references" && arg != "index" {
							add(a.Pos, "unknown @relation argument "+arg)
						}
					}
				}
				continue
			}
			if scalarTypes[f.Type] {
				if hasAttr(f, "jsonString") {
					add(f.Pos, "@jsonString requires a named type or enum")
				}
			} else if _, ok := types[f.Type]; !ok {
				if _, ok := enums[f.Type]; !ok {
					add(f.Pos, "unknown field type "+f.Type)
				}
			} else if hasAttr(f, "jsonString") == false {
				add(f.Pos, "composite field "+f.Name+" must use @jsonString")
			}
			for _, a := range f.Attributes {
				switch a.Name {
				case "partitionKey", "sortKey", "id", "jsonString":
					if len(a.Args) > 0 {
						add(a.Pos, "@"+a.Name+" does not accept arguments")
					}
				case "default":
					defaultCount++
					if defaultCount > 1 {
						add(a.Pos, "field may declare only one @default")
					}
					generator := strings.ToLower(unquote(a.Args["0"]))
					if len(a.Args) != 1 || generator == "" {
						add(a.Pos, "@default requires exactly one ID generator")
					}
					if generator != "uuidv1" && generator != "uuidv4" && generator != "uuidv7" && generator != "serial" && generator != "objectid" {
						add(a.Pos, "unsupported @default ID generator "+generator)
					}
					if f.Name != m.Partition {
						add(a.Pos, "@default ID generator is only allowed on the partition key")
					}
					if f.Optional || f.List {
						add(a.Pos, "@default ID generator requires a required scalar field")
					}
					if generator == "serial" && f.Type != "Int" {
						add(a.Pos, "serial ID generator requires an Int field")
					}
					if (generator == "uuidv1" || generator == "uuidv4" || generator == "uuidv7" || generator == "objectid") && f.Type != "String" {
						add(a.Pos, generator+" ID generator requires a String field")
					}
				default:
					add(a.Pos, "unknown field attribute @"+a.Name)
				}
			}
		}
		for index := range m.Indexes {
			a := &m.Indexes[index]
			if a.Name == "" {
				add(a.Pos, "index name is required")
			}
			if a.Partition == "" && a.Kind != "LSI" {
				add(a.Pos, "GSI requires a partition field")
			}
			if a.Kind == "" {
				a.Kind = "GSI"
			}
			if a.Kind != "GSI" && a.Kind != "LSI" {
				add(a.Pos, "index type must be GSI or LSI")
			}
			if a.Kind == "LSI" {
				if a.Sort == "" {
					add(a.Pos, "LSI requires a sort field")
				}
				if m.Sort == "" {
					add(a.Pos, "LSI requires a table sort key")
				}
				if a.Partition == "" {
					a.Partition = m.Partition
				} else if a.Partition != m.Partition {
					add(a.Pos, "LSI partition must match the table partition key")
				}
			}
			if a.Partition != "" {
				if m.Field(a.Partition) == nil {
					add(a.Pos, "index references unknown partition field "+a.Partition)
				} else if !validKeyField(m.Field(a.Partition), s) {
					add(a.Pos, "index partition field must be a required String, Int, Float, DateTime, or Bytes field")
				}
			}
			if a.Sort != "" {
				if m.Field(a.Sort) == nil {
					add(a.Pos, "index references unknown sort field "+a.Sort)
				} else if !validKeyField(m.Field(a.Sort), s) {
					add(a.Pos, "index sort field must be a required String, Int, Float, DateTime, or Bytes field")
				}
			}
		}
		seenI := map[string]bool{}
		for _, ix := range m.Indexes {
			if seenI[ix.Name] {
				add(ix.Pos, "duplicate index name "+ix.Name)
			}
			seenI[ix.Name] = true
		}
		for _, r := range m.Relations {
			target := models[r.Target]
			if target == nil {
				add(r.Pos, "unknown relation model "+r.Target)
				continue
			}
			if r.Local == "" || r.Remote == "" {
				add(r.Pos, "@relation requires fields: [...] and references: [...]")
				continue
			}
			if r.Many && r.Optional {
				add(r.Pos, "to-many relation fields cannot be optional; an empty result is represented by an empty page")
			}
			if strings.Contains(r.Local, ",") || strings.Contains(r.Remote, ",") {
				add(r.Pos, "v1 relations support one local and one referenced field")
			}
			if m.Field(r.Local) == nil {
				add(r.Pos, "relation fields references unknown local field "+r.Local)
			} else if !r.Many && m.Field(r.Local).Optional != r.Optional {
				add(r.Pos, "to-one relation optionality must match its local key field")
			}
			if target.Field(r.Remote) == nil {
				add(r.Pos, "relation references unknown target field "+r.Remote)
			} else if m.Field(r.Local) != nil && m.Field(r.Local).Type != target.Field(r.Remote).Type {
				add(r.Pos, "relation local and referenced fields must have the same type")
			}
			if r.Many {
				found := false
				for _, ix := range target.Indexes {
					if ix.Partition == r.Remote && (r.Index == "" || ix.Name == r.Index) {
						found = true
					}
				}
				if !found {
					add(r.Pos, fmt.Sprintf("relation %s.%s needs a target index partitioned by %s", m.Name, r.FieldName, r.Remote))
				}
			} else if target.Partition != r.Remote {
				add(r.Pos, fmt.Sprintf("to-one relation %s.%s must reference target partition key %s", m.Name, r.FieldName, target.Partition))
			} else if target.Sort != "" {
				add(r.Pos, "to-one relation to a composite primary key is not supported in v1")
			}
		}
	}
	for _, t := range s.Types {
		seen := map[string]bool{}
		for _, f := range t.Fields {
			if seen[f.Name] {
				add(f.Pos, "duplicate field "+f.Name)
			}
			seen[f.Name] = true
			if !scalarTypes[f.Type] {
				if _, ok := enums[f.Type]; !ok {
					if _, ok := types[f.Type]; !ok {
						add(f.Pos, "unknown JSON field type "+f.Type)
					}
				}
			}
			if len(f.Attributes) > 0 {
				add(f.Pos, "attributes are not supported on JSON type fields")
			}
		}
	}
	// Direct by-value composite cycles cannot be represented by Go structs.
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visiting[name] = true
		typ := types[name]
		for _, f := range typ.Fields {
			if scalarTypes[f.Type] || f.Optional || f.List {
				continue
			}
			if _, ok := enums[f.Type]; ok {
				continue
			}
			if _, ok := types[f.Type]; ok {
				if visiting[f.Type] {
					add(f.Pos, "recursive JSON types must use an optional or list field")
				} else {
					visit(f.Type)
				}
			}
		}
		delete(visiting, name)
		visited[name] = true
	}
	for name := range types {
		visit(name)
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Position.Line == ds[j].Position.Line {
			return ds[i].Position.Column < ds[j].Position.Column
		}
		return ds[i].Position.Line < ds[j].Position.Line
	})
	return ds
}
func hasAttr(f Field, name string) bool {
	for _, a := range f.Attributes {
		if a.Name == name {
			return true
		}
	}
	return false
}
func validKeyField(f *Field, s *Schema) bool {
	if f == nil || f.Optional || f.List {
		return false
	}
	if scalarTypes[f.Type] {
		return f.Type != "Boolean"
	}
	for _, e := range s.Enums {
		if e.Name == f.Type {
			return true
		}
	}
	return false
}
func goIdentifier(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b strings.Builder
	for _, p := range parts {
		r := []rune(p)
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		b.WriteString(string(r))
	}
	out := b.String()
	if out == "" {
		return "X"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		out = "X" + out
	}
	for _, pair := range [][2]string{{"Id", "ID"}, {"Url", "URL"}, {"Http", "HTTP"}, {"Aws", "AWS"}, {"Json", "JSON"}, {"Api", "API"}} {
		out = strings.ReplaceAll(out, pair[0], pair[1])
	}
	return out
}
func positionFieldName(m Model, pos Position) string {
	for _, f := range m.Fields {
		if f.Pos == pos {
			return f.Name
		}
	}
	return "?"
}
func positionFieldNameComposite(c Composite, pos Position) string {
	for _, f := range c.Fields {
		if f.Pos == pos {
			return f.Name
		}
	}
	return "?"
}
