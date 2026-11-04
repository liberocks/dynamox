// Package schema parses and validates dynamox schema files.
package schema

import "fmt"

type Position struct{ Line, Column int }
type Diagnostic struct {
	Position Position
	Message  string
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("%d:%d: %s", d.Position.Line, d.Position.Column, d.Message)
}

type Attribute struct {
	Name string
	Args map[string]string
	Pos  Position
}
type Field struct {
	Name, Type     string
	List, Optional bool
	Attributes     []Attribute
	Pos            Position
	Comment        string
}
type Index struct {
	Name, Partition, Sort, Kind string
	Pos                         Position
}
type Relation struct {
	FieldName, Target, Local, Remote, Index string
	Many, Optional                          bool
	Pos                                     Position
}
type Model struct {
	Name, Table, Partition, Sort string
	Fields                       []Field
	Indexes                      []Index
	Relations                    []Relation
	Pos                          Position
	Comment                      string
}
type Composite struct {
	Name    string
	Fields  []Field
	Pos     Position
	Comment string
}
type Enum struct {
	Name    string
	Values  []string
	Pos     Position
	Comment string
}
type Schema struct {
	Models []Model
	Types  []Composite
	Enums  []Enum
}

func (s *Schema) Model(name string) *Model {
	for i := range s.Models {
		if s.Models[i].Name == name {
			return &s.Models[i]
		}
	}
	return nil
}
func (m *Model) Field(name string) *Field {
	for i := range m.Fields {
		if m.Fields[i].Name == name {
			return &m.Fields[i]
		}
	}
	return nil
}
