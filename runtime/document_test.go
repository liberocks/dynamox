package runtime

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestJSONLikeValuesRoundTripAndRejectInvalidValues(t *testing.T) {
	if encoded, err := MarshalJSON(nil); err != nil {
		t.Fatalf("top-level nil should encode as NULL: %v", err)
	} else if _, ok := encoded.(*types.AttributeValueMemberNULL); !ok {
		t.Fatalf("top-level nil encoded as %T", encoded)
	}
	var nilPointer *string
	if _, err := MarshalJSON(nilPointer); err != nil {
		t.Fatalf("nil pointer should encode as NULL: %v", err)
	}
	var nilMap map[string]any
	if _, err := MarshalJSON(nilMap); err != nil {
		t.Fatalf("nil map should encode as NULL: %v", err)
	}
	if _, err := MarshalJSON([]any{"first", json.Number("2")}); err != nil {
		t.Fatalf("non-empty list should encode: %v", err)
	}
	value := map[string]any{"nil": nil, "bool": true, "text": "x", "int": int64(12), "uint": uint64(13), "float": 1.25, "precise": json.Number("12345678901234567890"), "list": []any{}, "map": map[string]any{}}
	encoded, err := MarshalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := encoded.(*types.AttributeValueMemberM)
	if !ok {
		t.Fatalf("expected native map, got %T", encoded)
	}
	if _, ok := m.Value["nil"].(*types.AttributeValueMemberNULL); !ok {
		t.Fatalf("null did not remain native NULL: %#v", m.Value["nil"])
	}
	if list, ok := m.Value["list"].(*types.AttributeValueMemberL); !ok || len(list.Value) != 0 {
		t.Fatalf("empty list did not remain an empty list: %#v", m.Value["list"])
	}
	var decoded any
	if err := UnmarshalJSON(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	number := decoded.(map[string]interface{})["precise"].(attributevalue.Number)
	if string(number) != "12345678901234567890" {
		t.Fatalf("number precision changed: %s", number)
	}
	if _, err := MarshalJSON(json.Number("bad")); err == nil {
		t.Fatal("invalid JSON number should fail")
	}
	if _, err := MarshalJSON(math.NaN()); err == nil {
		t.Fatal("NaN should fail")
	}
	if _, err := MarshalJSON(map[int]string{1: "x"}); err == nil {
		t.Fatal("non-string object key should fail")
	}
	if _, err := MarshalJSON(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 string should fail")
	}
	if _, err := MarshalJSON(map[string]any{string([]byte{0xff}): "x"}); err == nil {
		t.Fatal("invalid UTF-8 object key should fail")
	}
	if _, err := MarshalJSON(struct{ Value string }{"x"}); err == nil {
		t.Fatal("struct is not a JSON-like dynamic value")
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := MarshalJSON(cyclic); err == nil {
		t.Fatal("cyclic object should fail")
	}
	cyclicList := make([]any, 1)
	cyclicList[0] = cyclicList
	if _, err := MarshalJSON(cyclicList); err == nil {
		t.Fatal("cyclic list should fail")
	}
	var deep any = "leaf"
	for i := 0; i < maxJSONDocumentDepth+1; i++ {
		deep = map[string]any{"child": deep}
	}
	if _, err := MarshalJSON(deep); err == nil {
		t.Fatal("overly nested JSON-like value should fail")
	}
	var deepList any = "leaf"
	for i := 0; i < maxJSONDocumentDepth+1; i++ {
		deepList = []any{deepList}
	}
	if _, err := MarshalJSON(deepList); err == nil {
		t.Fatal("overly nested JSON-like list should fail")
	}
	var pointerCycle any
	pointer := &pointerCycle
	pointerCycle = pointer
	if _, err := MarshalJSON(pointer); err == nil {
		t.Fatal("cyclic pointer should fail")
	}
	var nilSlice []any
	if _, err := MarshalJSON(nilSlice); err != nil {
		t.Fatalf("nil slice should encode as NULL: %v", err)
	}
}

func TestUnmarshalJSONRejectsNonJSONDynamoAttributes(t *testing.T) {
	for name, value := range map[string]types.AttributeValue{
		"string set":      &types.AttributeValueMemberSS{Value: []string{"x"}},
		"number set":      &types.AttributeValueMemberNS{Value: []string{"1"}},
		"binary":          &types.AttributeValueMemberB{Value: []byte("x")},
		"binary set":      &types.AttributeValueMemberBS{Value: [][]byte{[]byte("x")}},
		"nil child":       &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"x": nil}},
		"invalid number":  &types.AttributeValueMemberN{Value: "NaN"},
		"false null":      &types.AttributeValueMemberNULL{Value: false},
		"invalid string":  &types.AttributeValueMemberS{Value: string([]byte{0xff})},
		"invalid map key": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{string([]byte{0xff}): &types.AttributeValueMemberS{Value: "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			var decoded any
			if err := UnmarshalJSON(value, &decoded); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
	cyclic := &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}
	cyclic.Value["self"] = cyclic
	var decoded any
	if err := UnmarshalJSON(cyclic, &decoded); err == nil {
		t.Fatal("cyclic DynamoDB document should be rejected")
	}
	var nilMember *types.AttributeValueMemberS
	if err := UnmarshalJSON(nilMember, &decoded); err == nil {
		t.Fatal("nil DynamoDB attribute pointer should be rejected")
	}
	if err := UnmarshalJSON(&types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "valid"}}}, &decoded); err != nil {
		t.Fatalf("non-empty native list should decode: %v", err)
	}
	badList := &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberSS{Value: []string{"x"}}}}
	if err := UnmarshalJSON(badList, &decoded); err == nil {
		t.Fatal("non-JSON value nested in a DynamoDB list should be rejected")
	}
	var deep types.AttributeValue = &types.AttributeValueMemberS{Value: "leaf"}
	for i := 0; i < maxJSONDocumentDepth+1; i++ {
		deep = &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"child": deep}}
	}
	if err := UnmarshalJSON(deep, &decoded); err == nil || !strings.Contains(err.Error(), "nesting limit") {
		t.Fatalf("overly nested DynamoDB Json should fail with a depth error: %v", err)
	}
	var deepList types.AttributeValue = &types.AttributeValueMemberS{Value: "leaf"}
	for i := 0; i < maxJSONDocumentDepth+1; i++ {
		deepList = &types.AttributeValueMemberL{Value: []types.AttributeValue{deepList}}
	}
	if err := UnmarshalJSON(deepList, &decoded); err == nil || !strings.Contains(err.Error(), "nesting limit") {
		t.Fatalf("overly nested DynamoDB Json list should fail with a depth error: %v", err)
	}
}
