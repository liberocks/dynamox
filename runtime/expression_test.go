package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestExpressionBuildersAliasInputAndMergeCollisions(t *testing.T) {
	attribute := "status) OR attribute_exists(secret) OR (#x"
	first, err := Compare(attribute, Equal, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compare("display.name", BeginsWith, "pre")
	if err != nil {
		t.Fatal(err)
	}
	combined, err := And(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(combined.Text, attribute) || combined.Names["#dx_attr"] != attribute || combined.Names["#dx_attr_1"] != "display.name" {
		t.Fatalf("attribute input was not safely aliased: %#v", combined)
	}
	if combined.Values[":dx_value"] != "first" || combined.Values[":dx_value_1"] != "pre" {
		t.Fatalf("colliding values were not preserved: %#v", combined.Values)
	}
	if _, _, text, err := expressionMaps(&combined); err != nil || text != combined.Text {
		t.Fatalf("combined expression failed validation: %q %v", text, err)
	}
	exists, err := AttributeExists("created at")
	if err != nil || exists.Names["#dx_attr"] != "created at" {
		t.Fatalf("AttributeExists did not alias its input: %#v %v", exists, err)
	}
	missing, err := AttributeNotExists("key")
	if err != nil || !strings.Contains(missing.Text, "attribute_not_exists") {
		t.Fatalf("AttributeNotExists failed: %#v %v", missing, err)
	}
	if _, err := Compare("", Equal, 1); err == nil {
		t.Fatal("Compare accepted an empty attribute")
	}
	if _, err := Compare(string([]byte{0xff}), Equal, 1); err == nil {
		t.Fatal("Compare accepted an invalid UTF-8 attribute")
	}
	if _, err := Compare("status", Operator("= OR attribute_exists(secret)"), "x"); err == nil {
		t.Fatal("Compare accepted an arbitrary operator")
	}
	if _, err := AttributeExists(""); err == nil {
		t.Fatal("AttributeExists accepted an empty attribute")
	}
	if _, err := AttributeNotExists(""); err == nil {
		t.Fatal("AttributeNotExists accepted an empty attribute")
	}
	if _, err := And(); err == nil {
		t.Fatal("And accepted no expressions")
	}
	if _, err := And(Expression{}); err == nil {
		t.Fatal("And accepted an empty expression")
	}
	if _, err := And(Expression{Text: "#missing = :value", Values: map[string]any{":value": 1}}); err == nil {
		t.Fatal("And accepted an invalid expression")
	}
	tooLong := Expression{Text: strings.Repeat("x", maxExpressionBytes/2+1)}
	if _, err := And(tooLong, tooLong); err == nil {
		t.Fatal("And accepted a merged expression over the size limit")
	}
}

func TestExpressionValidationRejectsInvalidOrUnboundPlaceholders(t *testing.T) {
	cases := []Expression{
		{Text: "#name = :value", Values: map[string]any{":value": 1}},
		{Text: "#name = :value", Names: map[string]string{"#name": "name"}},
		{Text: "attribute_exists(name)", Names: map[string]string{"name": "name"}},
		{Text: "attribute_exists(name)", Names: map[string]string{"#name": ""}},
		{Text: "attribute_exists(#name)", Names: map[string]string{"#name": string([]byte{0xff})}},
		{Text: "attribute_exists(#name)", Names: map[string]string{"#name": "name", "#unused": "other"}},
		{Text: "#name = :value", Names: map[string]string{"#name": "name"}, Values: map[string]any{"value": 1}},
		{Text: "#name = :value", Names: map[string]string{"#name": "name"}, Values: map[string]any{":unused": 1}},
		{Text: "#name = :value", Names: map[string]string{"#name": "name"}, Values: map[string]any{":value": 1, "bad": 2}},
		{Text: "#name = :value", Names: map[string]string{"#name": "name"}, Values: map[string]any{":value": 1, ":unused": 2}},
		{Text: "#"},
		{Text: "attribute_exists(#" + strings.Repeat("x", maxExpressionPlaceholderBytes) + ")", Names: map[string]string{"#" + strings.Repeat("x", maxExpressionPlaceholderBytes): "name"}},
		{Text: "  "},
		{Text: strings.Repeat("x", maxExpressionBytes+1)},
		{Text: string([]byte{0xff})},
		{Names: map[string]string{"#name": "name"}},
	}
	for i, expression := range cases {
		if err := validateExpression(&expression); err == nil {
			t.Errorf("validateExpression accepted invalid case %d: %#v", i, expression)
		}
	}
	if err := validateExpression(nil); err != nil {
		t.Fatalf("nil expression should be valid: %v", err)
	}
	if err := validateExpression(&Expression{}); err != nil {
		t.Fatalf("empty expression should be a no-op: %v", err)
	}
	if err := validateExpression(&Expression{Text: "attribute_exists(#name)", Names: map[string]string{"#name": "name"}}); err != nil {
		t.Fatalf("valid expression rejected: %v", err)
	}
	if err := validateExpression(&Expression{Text: "attribute_exists(#name)", Names: map[string]string{"#na-me": "name"}}); err == nil {
		t.Fatal("validateExpression accepted a malformed placeholder key")
	}
	if err := validateExpression(&Expression{Text: "attribute_exists(#name)", Names: map[string]string{"#name": "name"}, Values: map[string]any{"bad": 1}}); err == nil {
		t.Fatal("validateExpression accepted an invalid value placeholder key")
	}
}

func TestExpressionPlaceholderWalkAndReplacement(t *testing.T) {
	seen := 0
	if err := walkPlaceholders("attribute_exists(#name) AND #name = :value", func(string) error { seen++; return nil }); err != nil || seen != 3 {
		t.Fatalf("placeholder walk got count %d, err %v", seen, err)
	}
	if err := walkPlaceholders("#", func(string) error { return nil }); err == nil {
		t.Fatal("placeholder walk accepted an empty placeholder")
	}
	wantErr := errors.New("visitor failed")
	if err := walkPlaceholders(":value", func(string) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatal("placeholder walk hid visitor errors")
	}
	if got := replacePlaceholders("#name = :value", map[string]string{"#name": "#new", ":value": ":new"}); got != "#new = :new" {
		t.Fatalf("replacePlaceholders returned %q", got)
	}
	if got := replacePlaceholders("#", nil); got != "#" {
		t.Fatalf("replacePlaceholders changed a malformed token to %q", got)
	}
	if got := replacePlaceholders("#missing", nil); got != "#missing" {
		t.Fatalf("replacePlaceholders changed an unmapped token to %q", got)
	}
	for _, tc := range []struct {
		value  string
		prefix byte
		valid  bool
	}{
		{"#x", '#', true}, {":_1", ':', true}, {"#", '#', false}, {":a-b", ':', false}, {":a", '#', false},
	} {
		if got := validPlaceholder(tc.value, tc.prefix); got != tc.valid {
			t.Errorf("validPlaceholder(%q, %q)=%v", tc.value, tc.prefix, got)
		}
	}
	if !placeholderCharacter('a') || !placeholderCharacter('7') || !placeholderCharacter('_') || placeholderCharacter('-') {
		t.Fatal("placeholder character classifier returned an unexpected result")
	}
}
