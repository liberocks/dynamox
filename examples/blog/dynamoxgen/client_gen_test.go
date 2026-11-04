package dynamoxgen

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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
