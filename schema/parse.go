package schema

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokenKind uint8

const (
	ident tokenKind = iota
	stringLit
	symbol
	comment
	eof
)

type token struct {
	text string
	kind tokenKind
	pos  Position
}

func lex(src string) ([]token, error) {
	var out []token
	line, col := 1, 1
	for i := 0; i < len(src); {
		c := src[i]
		if c == '\n' {
			line++
			col = 1
			i++
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			col++
			i++
			continue
		}
		start := Position{line, col}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			j := i + 2
			for j < len(src) && src[j] != '\n' {
				j++
			}
			out = append(out, token{src[i:j], comment, start})
			col += j - i
			i = j
			continue
		}
		if c == '"' {
			j := i + 1
			escaped := false
			for j < len(src) {
				if !escaped && src[j] == '"' {
					j++
					break
				}
				if !escaped && src[j] == '\\' {
					escaped = true
				} else {
					escaped = false
				}
				if src[j] == '\n' {
					return nil, fmt.Errorf("%d:%d: newline in string", line, col)
				}
				j++
			}
			if j > len(src) || src[j-1] != '"' {
				return nil, fmt.Errorf("%d:%d: unterminated string", line, col)
			}
			out = append(out, token{src[i:j], stringLit, start})
			col += j - i
			i = j
			continue
		}
		if unicode.IsLetter(rune(c)) || c == '_' {
			j := i + 1
			for j < len(src) {
				r := rune(src[j])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
					break
				}
				j++
			}
			out = append(out, token{src[i:j], ident, start})
			col += j - i
			i = j
			continue
		}
		if c == '@' && i+1 < len(src) && src[i+1] == '@' {
			out = append(out, token{"@@", symbol, start})
			col += 2
			i += 2
			continue
		}
		if strings.ContainsRune("{}[](),:@?=", rune(c)) {
			out = append(out, token{string(c), symbol, start})
			col++
			i++
			continue
		}
		return nil, fmt.Errorf("%d:%d: unexpected character %q", line, col, c)
	}
	out = append(out, token{"", eof, Position{line, col}})
	return out, nil
}

type parser struct {
	toks     []token
	i        int
	comments map[int][]string
}

func Parse(src string) (*Schema, error) {
	t, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := parser{toks: t, comments: map[int][]string{}}
	for _, x := range t {
		if x.kind == comment {
			p.comments[x.pos.Line] = append(p.comments[x.pos.Line], strings.TrimSpace(strings.TrimPrefix(x.text, "//")))
		}
	}
	s := &Schema{}
	for p.peek().kind != eof {
		if p.peek().kind == comment {
			p.i++
			continue
		}
		kind := p.take().text
		name, err := p.want(ident)
		if err != nil {
			return nil, err
		}
		pos := p.toks[p.i-1].pos
		doc := p.comments[pos.Line-1]
		if _, err = p.wantText("{"); err != nil {
			return nil, err
		}
		switch kind {
		case "model":
			m := Model{Name: name.text, Pos: pos, Comment: strings.Join(doc, "\n")}
			if err := p.parseModel(&m); err != nil {
				return nil, err
			}
			s.Models = append(s.Models, m)
		case "type":
			v := Composite{Name: name.text, Pos: pos, Comment: strings.Join(doc, "\n")}
			if err := p.parseComposite(&v); err != nil {
				return nil, err
			}
			s.Types = append(s.Types, v)
		case "enum":
			e := Enum{Name: name.text, Pos: pos, Comment: strings.Join(doc, "\n")}
			if err := p.parseEnum(&e); err != nil {
				return nil, err
			}
			s.Enums = append(s.Enums, e)
		default:
			return nil, fmt.Errorf("%d:%d: expected model, type, or enum block", pos.Line, pos.Column)
		}
	}
	return s, nil
}
func (p *parser) peek() token {
	if p.i >= len(p.toks) {
		return token{kind: eof}
	}
	return p.toks[p.i]
}
func (p *parser) take() token {
	t := p.peek()
	if t.kind != eof {
		p.i++
	}
	return t
}
func (p *parser) want(k tokenKind) (token, error) {
	t := p.take()
	if t.kind != k {
		return t, fmt.Errorf("%d:%d: expected %v, got %q", t.pos.Line, t.pos.Column, k, t.text)
	}
	return t, nil
}
func (p *parser) wantText(s string) (token, error) {
	t := p.take()
	if t.text != s {
		return t, fmt.Errorf("%d:%d: expected %q, got %q", t.pos.Line, t.pos.Column, s, t.text)
	}
	return t, nil
}
func (p *parser) skipComments() {
	for p.peek().kind == comment {
		p.take()
	}
}
func (p *parser) parseModel(m *Model) error {
	for {
		p.skipComments()
		if p.peek().text == "}" {
			p.take()
			return nil
		}
		if p.peek().kind == eof {
			return fmt.Errorf("%d:%d: unclosed model %s", m.Pos.Line, m.Pos.Column, m.Name)
		}
		if p.peek().text == "@@" {
			if err := p.parseModelAttr(m); err != nil {
				return err
			}
			continue
		}
		name, err := p.want(ident)
		if err != nil {
			return err
		}
		typ, err := p.want(ident)
		if err != nil {
			return err
		}
		f := Field{Name: name.text, Type: typ.text, Pos: name.pos, Comment: strings.Join(p.comments[name.pos.Line-1], "\n")}
		if p.peek().text == "[" {
			p.take()
			f.List = true
			if _, e := p.wantText("]"); e != nil {
				return e
			}
		}
		if p.peek().text == "?" {
			p.take()
			f.Optional = true
		}
		for p.peek().text == "@" {
			p.take()
			a, e := p.parseAttr()
			if e != nil {
				return e
			}
			f.Attributes = append(f.Attributes, a)
			if a.Name == "partitionKey" {
				m.Partition = f.Name
			}
			if a.Name == "sortKey" {
				m.Sort = f.Name
			}
			if a.Name == "id" {
				m.Partition = f.Name
			}
		}
		m.Fields = append(m.Fields, f)
		for _, a := range f.Attributes {
			if a.Name == "relation" {
				r := Relation{FieldName: f.Name, Target: f.Type, Many: f.List, Optional: f.Optional, Pos: a.Pos, Local: a.Args["fields"], Remote: a.Args["references"], Index: strings.Trim(a.Args["index"], "\"")}
				m.Relations = append(m.Relations, r)
			}
		}
	}
}
func (p *parser) parseComposite(c *Composite) error {
	for {
		p.skipComments()
		if p.peek().text == "}" {
			p.take()
			return nil
		}
		n, e := p.want(ident)
		if e != nil {
			return e
		}
		t, e := p.want(ident)
		if e != nil {
			return e
		}
		f := Field{Name: n.text, Type: t.text, Pos: n.pos}
		if p.peek().text == "[" {
			p.take()
			f.List = true
			if _, e = p.wantText("]"); e != nil {
				return e
			}
		}
		if p.peek().text == "?" {
			p.take()
			f.Optional = true
		}
		for p.peek().text == "@" {
			p.take()
			a, e := p.parseAttr()
			if e != nil {
				return e
			}
			f.Attributes = append(f.Attributes, a)
		}
		c.Fields = append(c.Fields, f)
	}
}
func (p *parser) parseEnum(e *Enum) error {
	for {
		p.skipComments()
		if p.peek().text == "}" {
			p.take()
			return nil
		}
		v, err := p.want(ident)
		if err != nil {
			return err
		}
		e.Values = append(e.Values, v.text)
	}
}
func (p *parser) parseModelAttr(m *Model) error {
	p.take()
	a, err := p.want(ident)
	if err != nil {
		return err
	}
	if _, err = p.wantText("("); err != nil {
		return err
	}
	args, err := p.parseArgs()
	if err != nil {
		return err
	}
	switch a.text {
	case "table":
		for key := range args {
			if key != "name" && key != "0" {
				return fmt.Errorf("%d:%d: unknown @@table argument %q", a.pos.Line, a.pos.Column, key)
			}
		}
		m.Table = unquote(args["name"])
		if m.Table == "" {
			m.Table = unquote(args["0"])
		}
	case "index":
		for key := range args {
			if key != "name" && key != "partition" && key != "sort" && key != "type" {
				return fmt.Errorf("%d:%d: unknown @@index argument %q", a.pos.Line, a.pos.Column, key)
			}
		}
		ix := Index{Name: unquote(args["name"]), Partition: unquote(args["partition"]), Sort: unquote(args["sort"]), Kind: strings.ToUpper(unquote(args["type"])), Pos: a.pos}
		m.Indexes = append(m.Indexes, ix)
	default:
		return fmt.Errorf("%d:%d: unknown model attribute @@%s", a.pos.Line, a.pos.Column, a.text)
	}
	return nil
}
func (p *parser) parseAttr() (Attribute, error) {
	n, err := p.want(ident)
	if err != nil {
		return Attribute{}, err
	}
	a := Attribute{Name: n.text, Args: map[string]string{}, Pos: n.pos}
	if p.peek().text != "(" {
		return a, nil
	}
	p.take()
	args, err := p.parseArgs()
	a.Args = args
	return a, err
}
func (p *parser) parseArgs() (map[string]string, error) {
	r := map[string]string{}
	idx := 0
	for p.peek().text != ")" {
		if p.peek().kind == eof {
			return nil, fmt.Errorf("%d:%d: unclosed argument list", p.peek().pos.Line, p.peek().pos.Column)
		}
		key := ""
		val := p.take()
		if val.kind == ident && p.peek().text == ":" {
			key = val.text
			p.take()
			val = p.take()
		} else {
			key = fmt.Sprint(idx)
			idx++
		}
		if val.text == "[" {
			var xs []string
			for p.peek().text != "]" {
				x := p.take()
				if x.text == "," {
					continue
				}
				xs = append(xs, x.text)
			}
			p.take()
			r[key] = strings.Join(xs, ",")
		} else {
			r[key] = val.text
		}
		if p.peek().text == "," {
			p.take()
		} else if p.peek().text != ")" {
			return nil, fmt.Errorf("%d:%d: expected comma or )", p.peek().pos.Line, p.peek().pos.Column)
		}
	}
	p.take()
	return r, nil
}
func unquote(s string) string {
	if x, e := strconv.Unquote(s); e == nil {
		return x
	}
	return s
}
