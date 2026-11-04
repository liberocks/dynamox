package runtime

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// MarshalJSON encodes JSON-like values as native DynamoDB values. Nil is a
// DynamoDB NULL; empty maps and slices remain empty maps and lists.
func MarshalJSON(value any) (types.AttributeValue, error) {
	normalized, err := normalizeJSON(reflect.ValueOf(value), map[visit]bool{})
	if err != nil {
		return nil, err
	}
	return attributevalue.Marshal(normalized)
}

// UnmarshalJSON decodes JSON-compatible DynamoDB documents while preserving numbers.
func UnmarshalJSON(value types.AttributeValue, out any) error {
	if err := validateJSONAttribute(value, map[visit]bool{}); err != nil {
		return err
	}
	return attributevalue.NewDecoder(func(o *attributevalue.DecoderOptions) { o.UseNumber = true }).Decode(value, out)
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func normalizeJSON(v reflect.Value, active map[visit]bool) (any, error) {
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
	if v.Type() == reflect.TypeOf(json.Number("")) {
		n := v.Interface().(json.Number)
		if _, err := strconv.ParseFloat(string(n), 64); err != nil {
			return nil, fmt.Errorf("dynamox: invalid JSON number %q", n)
		}
		return attributevalue.Number(n), nil
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		return v.String(), nil
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
			x, err := normalizeJSON(iter.Value(), active)
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
			x, err := normalizeJSON(v.Index(i), active)
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

func validateJSONAttribute(value types.AttributeValue, active map[visit]bool) error {
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
		for _, child := range v.Value {
			if err := validateJSONAttribute(child, active); err != nil {
				return err
			}
		}
	case *types.AttributeValueMemberL:
		for _, child := range v.Value {
			if err := validateJSONAttribute(child, active); err != nil {
				return err
			}
		}
	case *types.AttributeValueMemberS, *types.AttributeValueMemberN, *types.AttributeValueMemberBOOL, *types.AttributeValueMemberNULL:
		return nil
	default:
		return fmt.Errorf("dynamox: unsupported DynamoDB Json attribute %T", value)
	}
	return nil
}
