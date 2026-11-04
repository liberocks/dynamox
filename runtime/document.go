package runtime

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const maxJSONDocumentDepth = 32

// MarshalJSON encodes JSON-like values as native DynamoDB values. Nil is a
// DynamoDB NULL; empty maps and slices remain empty maps and lists.
func MarshalJSON(value any) (types.AttributeValue, error) {
	normalized, err := normalizeJSON(reflect.ValueOf(value), map[visit]bool{}, 0)
	if err != nil {
		return nil, err
	}
	return attributevalue.Marshal(normalized)
}

// UnmarshalJSON decodes JSON-compatible DynamoDB documents while preserving numbers.
func UnmarshalJSON(value types.AttributeValue, out any) error {
	if err := validateJSONAttribute(value, map[visit]bool{}, 0); err != nil {
		return err
	}
	return attributevalue.NewDecoder(func(o *attributevalue.DecoderOptions) { o.UseNumber = true }).Decode(value, out)
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func normalizeJSON(v reflect.Value, active map[visit]bool, depth int) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, nil
		}
		if v.Kind() == reflect.Pointer {
			id := visit{v.Type(), v.Pointer()}
			if active[id] {
				return nil, fmt.Errorf("dynamox: cyclic JSON-like value")
			}
			active[id] = true
			defer delete(active, id)
		}
		v = v.Elem()
	}
	if v.Type() == reflect.TypeOf(json.Number("")) || v.Type() == reflect.TypeOf(attributevalue.Number("")) {
		n := v.String()
		var parsed json.Number
		if err := json.Unmarshal([]byte(n), &parsed); err != nil || string(parsed) != n {
			return nil, fmt.Errorf("dynamox: invalid JSON number %q", n)
		}
		return attributevalue.Number(n), nil
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		value := v.String()
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("dynamox: JSON-like strings must be valid UTF-8")
		}
		return value, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		n := v.Float()
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("dynamox: JSON-like numbers must be finite")
		}
		return n, nil
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}
		if depth >= maxJSONDocumentDepth {
			return nil, fmt.Errorf("dynamox: JSON-like value exceeds DynamoDB's %d-level nesting limit", maxJSONDocumentDepth)
		}
		if v.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("dynamox: JSON-like object keys must be strings")
		}
		id := visit{v.Type(), v.Pointer()}
		if active[id] {
			return nil, fmt.Errorf("dynamox: cyclic JSON-like value")
		}
		active[id] = true
		defer delete(active, id)
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			if !utf8.ValidString(iter.Key().String()) {
				return nil, fmt.Errorf("dynamox: JSON-like object keys must be valid UTF-8")
			}
			x, err := normalizeJSON(iter.Value(), active, depth+1)
			if err != nil {
				return nil, err
			}
			out[iter.Key().String()] = x
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, nil
		}
		if depth >= maxJSONDocumentDepth {
			return nil, fmt.Errorf("dynamox: JSON-like value exceeds DynamoDB's %d-level nesting limit", maxJSONDocumentDepth)
		}
		if v.Kind() == reflect.Slice {
			id := visit{v.Type(), v.Pointer()}
			if active[id] {
				return nil, fmt.Errorf("dynamox: cyclic JSON-like value")
			}
			active[id] = true
			defer delete(active, id)
		}
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			x, err := normalizeJSON(v.Index(i), active, depth+1)
			if err != nil {
				return nil, fmt.Errorf("dynamox: JSON-like array index %d: %w", i, err)
			}
			out[i] = x
		}
		return out, nil
	default:
		return nil, fmt.Errorf("dynamox: unsupported JSON-like value %s", v.Type())
	}
}

func validateJSONAttribute(value types.AttributeValue, active map[visit]bool, depth int) error {
	if value == nil {
		return fmt.Errorf("dynamox: nil DynamoDB attribute in Json document")
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return fmt.Errorf("dynamox: nil DynamoDB attribute in Json document")
		}
		id := visit{v.Type(), v.Pointer()}
		if active[id] {
			return fmt.Errorf("dynamox: cyclic DynamoDB Json document")
		}
		active[id] = true
		defer delete(active, id)
	}
	switch v := value.(type) {
	case *types.AttributeValueMemberM:
		if depth >= maxJSONDocumentDepth {
			return fmt.Errorf("dynamox: DynamoDB Json document exceeds the %d-level nesting limit", maxJSONDocumentDepth)
		}
		for key, child := range v.Value {
			if !utf8.ValidString(key) {
				return fmt.Errorf("dynamox: DynamoDB Json object keys must be valid UTF-8")
			}
			if err := validateJSONAttribute(child, active, depth+1); err != nil {
				return err
			}
		}
	case *types.AttributeValueMemberL:
		if depth >= maxJSONDocumentDepth {
			return fmt.Errorf("dynamox: DynamoDB Json document exceeds the %d-level nesting limit", maxJSONDocumentDepth)
		}
		for _, child := range v.Value {
			if err := validateJSONAttribute(child, active, depth+1); err != nil {
				return err
			}
		}
	case *types.AttributeValueMemberS:
		if !utf8.ValidString(v.Value) {
			return fmt.Errorf("dynamox: DynamoDB Json strings must be valid UTF-8")
		}
	case *types.AttributeValueMemberN:
		var number json.Number
		if err := json.Unmarshal([]byte(v.Value), &number); err != nil || string(number) != v.Value {
			return fmt.Errorf("dynamox: invalid DynamoDB Json number %q", v.Value)
		}
	case *types.AttributeValueMemberBOOL:
		return nil
	case *types.AttributeValueMemberNULL:
		if !v.Value {
			return fmt.Errorf("dynamox: DynamoDB Json NULL attribute must be true")
		}
		return nil
	default:
		return fmt.Errorf("dynamox: unsupported DynamoDB Json attribute %T", value)
	}
	return nil
}
