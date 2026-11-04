package dynamoxgen

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/runtime"
)

func TestJSONStringRoundTripAndMalformedValue(t *testing.T) {
	alt := ThemeOption{Value: "light"}
	item, err := encodeUser(User{ID: "u-1", Preferences: Preferences{Theme: "dark", Digest: true, Alternate: &alt}, Settings: Preferences{Theme: "light"}})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := item["preferences"].(*types.AttributeValueMemberS)
	if !ok || !strings.Contains(stored.Value, `"theme":"dark"`) {
		t.Fatalf("expected JSON string storage, got %#v", item["preferences"])
	}
	got, err := decodeUser(item)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preferences.Theme != "dark" || !got.Preferences.Digest || got.Preferences.Alternate == nil || got.Preferences.Alternate.Value != "light" {
		t.Fatalf("unexpected decoded JSON value: %#v", got.Preferences)
	}
	if got.Settings.Theme != "light" {
		t.Fatalf("second JSON field was not decoded: %#v", got.Settings)
	}
	item["preferences"] = &types.AttributeValueMemberS{Value: "{"}
	_, err = decodeUser(item)
	if err == nil || !strings.Contains(err.Error(), "User.preferences") {
		t.Fatalf("expected field-specific JSON decode error, got %v", err)
	}
}

func TestNativeDocumentsAndHeterogeneousJSONRoundTrip(t *testing.T) {
	nestedPayload := runtime.JSON(map[string]any{"score": json.Number("9007199254740993")})
	doc := DocumentMetadata{Source: "native", Provider: runtime.JSON(map[string]any{"ok": true}), Metadata: &nestedPayload}
	optionalNull := runtime.JSON(nil)
	payload := runtime.JSON(map[string]any{"items": []any{json.Number("12345678901234567890"), true, nil}, "emptyMap": map[string]any{}, "emptyList": []any{}})
	item, err := encodeUser(User{ID: "u-doc", DocumentPreferences: doc, OptionalDocumentPreferences: &doc, ProviderPayload: payload, OptionalPayload: &optionalNull, Payloads: []runtime.JSON{map[string]any{"ok": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item["documentPreferences"].(*types.AttributeValueMemberM); !ok {
		t.Fatalf("typed document was not stored as a map: %T", item["documentPreferences"])
	}
	if _, ok := item["optionalDocumentPreferences"].(*types.AttributeValueMemberM); !ok {
		t.Fatalf("optional typed document was not stored as a map: %T", item["optionalDocumentPreferences"])
	}
	if doc := item["documentPreferences"].(*types.AttributeValueMemberM); doc.Value["metadata"] == nil {
		t.Fatal("set optional nested Json field was omitted from the native document")
	}
	if _, ok := item["optionalPayload"].(*types.AttributeValueMemberNULL); !ok {
		t.Fatalf("explicit null was not stored as NULL: %T", item["optionalPayload"])
	}
	if _, ok := item["payloads"].(*types.AttributeValueMemberL); !ok {
		t.Fatalf("Json list was not stored as a list: %T", item["payloads"])
	}
	got, err := decodeUser(item)
	if err != nil {
		t.Fatal(err)
	}
	if got.DocumentPreferences.Source != "native" {
		t.Fatalf("native document did not round trip: %#v", got.DocumentPreferences)
	}
	if got.DocumentPreferences.Metadata == nil || string((*got.DocumentPreferences.Metadata).(map[string]interface{})["score"].(attributevalue.Number)) != "9007199254740993" {
		t.Fatalf("nested heterogeneous document did not retain its number: %#v", got.DocumentPreferences.Metadata)
	}
	if got.OptionalPayload == nil || *got.OptionalPayload != nil {
		t.Fatalf("explicit null did not remain distinguishable from absence: %#v", got.OptionalPayload)
	}
	decoded := got.ProviderPayload.(map[string]interface{})
	if string(decoded["items"].([]interface{})[0].(attributevalue.Number)) != "12345678901234567890" {
		t.Fatalf("JSON number precision changed: %#v", decoded)
	}
	emptyItem, err := encodeUser(User{ID: "empty", Payloads: []runtime.JSON{}})
	if err != nil {
		t.Fatal(err)
	}
	if list, ok := emptyItem["payloads"].(*types.AttributeValueMemberL); !ok || len(list.Value) != 0 {
		t.Fatalf("empty JSON list changed: %#v", emptyItem["payloads"])
	}
	absent, err := encodeUser(User{ID: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := absent["optionalPayload"]; ok {
		t.Fatal("nil optional Json field must be absent")
	}
	if _, ok := absent["optionalDocumentPreferences"]; ok {
		t.Fatal("nil optional typed document must be absent")
	}
	if doc := absent["documentPreferences"].(*types.AttributeValueMemberM); doc.Value["metadata"] != nil {
		t.Fatal("nil optional nested Json field should be absent from the native document")
	}
	bad := runtime.JSON(map[string]any{"unsupported": make(chan int)})
	if _, err = encodeUser(User{ID: "bad", ProviderPayload: bad}); err == nil {
		t.Fatal("unsupported JSON-like value must return an encoding error")
	}
	if _, err = encodeUser(User{ID: "bad-document", DocumentPreferences: DocumentMetadata{Provider: bad}}); err == nil {
		t.Fatal("unsupported nested document JSON must return an encoding error")
	}
}

func TestGeneratedCompositeAttributeCodecsAndDecodeErrors(t *testing.T) {
	value := ThemeOption{Value: "dark"}
	encoded, err := attributevalue.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ThemeOption
	if err := attributevalue.Unmarshal(encoded, &decoded); err != nil || decoded.Value != value.Value {
		t.Fatalf("nested composite codec: decoded=%#v err=%v", decoded, err)
	}
	var document DocumentMetadata
	if err := document.UnmarshalDynamoDBAttributeValue(&types.AttributeValueMemberS{Value: "wrong"}); err == nil {
		t.Fatal("expected native document map type error")
	}
	if err := document.UnmarshalDynamoDBAttributeValue(&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
		"source":   &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}},
		"provider": &types.AttributeValueMemberNULL{Value: true},
	}}); err == nil {
		t.Fatal("expected typed document decode error")
	}
	if err := document.UnmarshalDynamoDBAttributeValue(&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
		"source":   &types.AttributeValueMemberS{Value: "native"},
		"provider": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"nested": nil}},
	}}); err == nil {
		t.Fatal("expected nested Json decode error")
	}
}

func TestDateTimeStoredAsRFC3339AndDecoded(t *testing.T) {
	when := time.Date(2025, 2, 3, 4, 5, 6, 123000000, time.FixedZone("test", 3600))
	later := when.Add(time.Hour)
	item, err := encodePost(Post{ID: "p-1", CreatedAt: when, EditTimes: []time.Time{when, later}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item["createdAt"].(*types.AttributeValueMemberS); !ok {
		t.Fatalf("expected string DateTime attribute, got %T", item["createdAt"])
	}
	got, err := decodePost(item)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(when) {
		t.Fatalf("DateTime changed: got %s want %s", got.CreatedAt, when)
	}
	if len(got.EditTimes) != 2 || !got.EditTimes[1].Equal(later) {
		t.Fatalf("DateTime list changed: %#v", got.EditTimes)
	}
}
