package schema

import (
	"strings"
)

// Format normalizes spacing and indentation while leaving every comment on its original line.
func Format(src string) (string, error) {
	toks, err := lex(src)
	if err != nil {
		return "", err
	}
	lines := map[int][]token{}
	max := 0
	for _, t := range toks {
		if t.kind == eof {
			continue
		}
		lines[t.pos.Line] = append(lines[t.pos.Line], t)
		if t.pos.Line > max {
			max = t.pos.Line
		}
	}
	var out []string
	depth := 0
	for line := 1; line <= max; line++ {
		xs := lines[line]
		if len(xs) == 0 {
			continue
		}
		if xs[0].kind == comment {
			out = append(out, strings.Repeat("  ", depth)+strings.TrimSpace(xs[0].text))
			continue
		}
		for _, t := range xs {
			if t.text == "}" {
				depth--
			}
			if depth < 0 {
				depth = 0
			}
		}
		var b strings.Builder
		b.WriteString(strings.Repeat("  ", depth))
		prev := ""
		for _, t := range xs {
			if t.kind == comment {
				b.WriteByte(' ')
				b.WriteString(strings.TrimSpace(t.text))
				break
			}
			cur := t.text
			space := b.Len() > depth*2
			switch cur {
			case ",", ":", ")", "]", "?":
				space = false
			case "(", "[":
				space = false
			case "@", "@@":
				space = prev != ""
			}
			if prev == "@" || prev == "@@" {
				space = false
			}
			if prev == "," || prev == ":" {
				space = true
			}
			if prev == "(" || prev == "[" {
				space = false
			}
			if space {
				b.WriteByte(' ')
			}
			b.WriteString(cur)
			prev = cur
			if cur == "{" {
				depth++
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(out, "\n") + "\n", nil
}
