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
	pendingBlockSeparator := false
	for line := 1; line <= max; line++ {
		xs := lines[line]
		if len(xs) == 0 {
			continue
		}
		if xs[0].kind == comment {
			out = append(out, strings.Repeat("  ", depth)+strings.TrimSpace(xs[0].text))
			continue
		}
		depthBeforeLine := depth
		indentDepth := depth
		leading := true
		for _, t := range xs {
			if t.kind == comment {
				break
			}
			if leading && t.text == "}" {
				indentDepth--
			} else {
				leading = false
			}
		}
		if indentDepth < 0 {
			indentDepth = 0
		}
		for _, t := range xs {
			if t.kind == comment {
				continue
			}
			switch t.text {
			case "{":
				depth++
			case "}":
				depth--
			}
		}
		if depth < 0 {
			depth = 0
		}
		topLevelBlock := depthBeforeLine == 0 && isTopLevelBlock(xs)
		if pendingBlockSeparator && topLevelBlock {
			out = blankBeforeTrailingComments(out)
			pendingBlockSeparator = false
		}
		var b strings.Builder
		b.WriteString(strings.Repeat("  ", indentDepth))
		prev := ""
		for _, t := range xs {
			if t.kind == comment {
				b.WriteByte(' ')
				b.WriteString(strings.TrimSpace(t.text))
				break
			}
			cur := t.text
			space := b.Len() > indentDepth*2
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
		}
		out = append(out, strings.TrimRight(b.String(), " "))
		if (depthBeforeLine > 0 && depth == 0) || (topLevelBlock && depth == 0 && containsBracePair(xs)) {
			pendingBlockSeparator = true
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

func isTopLevelBlock(tokens []token) bool {
	if len(tokens) == 0 || tokens[0].kind != ident {
		return false
	}
	switch tokens[0].text {
	case "model", "type", "enum":
		return true
	default:
		return false
	}
}

func containsBracePair(tokens []token) bool {
	open, close := false, false
	for _, t := range tokens {
		open = open || t.text == "{"
		close = close || t.text == "}"
	}
	return open && close
}

func blankBeforeTrailingComments(lines []string) []string {
	insertAt := len(lines)
	for insertAt > 0 && strings.HasPrefix(strings.TrimSpace(lines[insertAt-1]), "//") {
		insertAt--
	}
	if insertAt == 0 || lines[insertAt-1] == "" {
		return lines
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:len(lines)-1])
	lines[insertAt] = ""
	return lines
}
