package runtime

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxExpressionBytes = 4 * 1024
const maxExpressionPlaceholderBytes = 255

// Compare builds a condition using an aliased attribute name and a marshaled
// value. Use it when either the attribute name or value comes from input.
func Compare(attribute string, operator Operator, value any) (Expression, error) {
	if attribute == "" || !utf8.ValidString(attribute) {
		return Expression{}, fmt.Errorf("dynamox: expression attribute name must be non-empty valid UTF-8")
	}
	switch operator {
	case Equal, Less, LessEqual, Greater, GreaterEqual:
		return Expression{
			Text:   "#dx_attr " + string(operator) + " :dx_value",
			Names:  map[string]string{"#dx_attr": attribute},
			Values: map[string]any{":dx_value": value},
		}, nil
	case BeginsWith:
		return Expression{
			Text:   "begins_with(#dx_attr, :dx_value)",
			Names:  map[string]string{"#dx_attr": attribute},
			Values: map[string]any{":dx_value": value},
		}, nil
	default:
		return Expression{}, fmt.Errorf("dynamox: unsupported expression operator %q", operator)
	}
}

// AttributeExists builds a condition with an aliased attribute name.
func AttributeExists(attribute string) (Expression, error) {
	return attributePresenceExpression(attribute, "attribute_exists")
}

// AttributeNotExists builds a condition with an aliased attribute name.
func AttributeNotExists(attribute string) (Expression, error) {
	return attributePresenceExpression(attribute, "attribute_not_exists")
}

func attributePresenceExpression(attribute, function string) (Expression, error) {
	if attribute == "" || !utf8.ValidString(attribute) {
		return Expression{}, fmt.Errorf("dynamox: expression attribute name must be non-empty valid UTF-8")
	}
	return Expression{
		Text:  function + "(#dx_attr)",
		Names: map[string]string{"#dx_attr": attribute},
	}, nil
}

// And combines expressions and renames colliding placeholders. Text supplied
// directly in an Expression must still be trusted DynamoDB expression syntax.
func And(expressions ...Expression) (Expression, error) {
	if len(expressions) == 0 {
		return Expression{}, fmt.Errorf("dynamox: And requires at least one expression")
	}
	out := Expression{Names: map[string]string{}, Values: map[string]any{}}
	parts := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		if err := validateExpression(&expression); err != nil {
			return Expression{}, err
		}
		if strings.TrimSpace(expression.Text) == "" {
			return Expression{}, fmt.Errorf("dynamox: cannot combine an empty expression")
		}
		replacements := make(map[string]string, len(expression.Names)+len(expression.Values))
		for _, key := range sortedKeys(expression.Names) {
			name := uniquePlaceholder(out.Names, key)
			replacements[key] = name
			out.Names[name] = expression.Names[key]
		}
		for _, key := range sortedKeys(expression.Values) {
			name := uniquePlaceholder(out.Values, key)
			replacements[key] = name
			out.Values[name] = expression.Values[key]
		}
		text := replacePlaceholders(expression.Text, replacements)
		parts = append(parts, "("+text+")")
	}
	out.Text = strings.Join(parts, " AND ")
	if err := validateExpression(&out); err != nil {
		return Expression{}, err
	}
	return out, nil
}

func validateExpression(expression *Expression) error {
	if expression == nil {
		return nil
	}
	if len(expression.Text) > maxExpressionBytes {
		return fmt.Errorf("dynamox: expression exceeds %d bytes", maxExpressionBytes)
	}
	if !utf8.ValidString(expression.Text) {
		return fmt.Errorf("dynamox: expression text must be valid UTF-8")
	}
	if strings.TrimSpace(expression.Text) == "" {
		if len(expression.Names) != 0 || len(expression.Values) != 0 {
			return fmt.Errorf("dynamox: expression placeholders require expression text")
		}
		if expression.Text != "" {
			return fmt.Errorf("dynamox: expression text must not be whitespace")
		}
		return nil
	}
	usedNames := map[string]bool{}
	usedValues := map[string]bool{}
	if err := walkPlaceholders(expression.Text, func(placeholder string) error {
		if placeholder[0] == '#' {
			if _, ok := expression.Names[placeholder]; !ok {
				return fmt.Errorf("dynamox: expression name placeholder %q has no binding", placeholder)
			}
			usedNames[placeholder] = true
			return nil
		}
		if _, ok := expression.Values[placeholder]; !ok {
			return fmt.Errorf("dynamox: expression value placeholder %q has no binding", placeholder)
		}
		usedValues[placeholder] = true
		return nil
	}); err != nil {
		return err
	}
	for placeholder, name := range expression.Names {
		if !validPlaceholder(placeholder, '#') {
			return fmt.Errorf("dynamox: invalid expression name placeholder %q", placeholder)
		}
		if name == "" || !utf8.ValidString(name) {
			return fmt.Errorf("dynamox: expression name binding %q must be non-empty valid UTF-8", placeholder)
		}
		if !usedNames[placeholder] {
			return fmt.Errorf("dynamox: expression name placeholder %q is unused", placeholder)
		}
	}
	for placeholder := range expression.Values {
		if !validPlaceholder(placeholder, ':') {
			return fmt.Errorf("dynamox: invalid expression value placeholder %q", placeholder)
		}
		if !usedValues[placeholder] {
			return fmt.Errorf("dynamox: expression value placeholder %q is unused", placeholder)
		}
	}
	return nil
}

func walkPlaceholders(text string, visit func(string) error) error {
	for i := 0; i < len(text); {
		prefix := text[i]
		if prefix != '#' && prefix != ':' {
			i++
			continue
		}
		end := i + 1
		for end < len(text) && placeholderCharacter(text[end]) {
			end++
		}
		if end == i+1 {
			return fmt.Errorf("dynamox: invalid expression placeholder at byte %d", i)
		}
		if err := visit(text[i:end]); err != nil {
			return err
		}
		i = end
	}
	return nil
}

func replacePlaceholders(text string, replacements map[string]string) string {
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		prefix := text[i]
		if prefix != '#' && prefix != ':' {
			out.WriteByte(text[i])
			i++
			continue
		}
		end := i + 1
		for end < len(text) && placeholderCharacter(text[end]) {
			end++
		}
		if end == i+1 {
			out.WriteByte(text[i])
			i++
			continue
		}
		placeholder := text[i:end]
		replacement, ok := replacements[placeholder]
		if !ok {
			out.WriteString(placeholder)
			i = end
			continue
		}
		out.WriteString(replacement)
		i = end
	}
	return out.String()
}

func validPlaceholder(placeholder string, prefix byte) bool {
	if len(placeholder) < 2 || len(placeholder) > maxExpressionPlaceholderBytes || placeholder[0] != prefix {
		return false
	}
	for i := 1; i < len(placeholder); i++ {
		if !placeholderCharacter(placeholder[i]) {
			return false
		}
	}
	return true
}

func placeholderCharacter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
