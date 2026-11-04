package runtime

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func resetObjectIDStateForTest() {
	objectIDState.once = sync.Once{}
	objectIDState.process = [5]byte{}
	objectIDState.counter.Store(0)
	objectIDState.err = nil
}

func TestGenerateIDFormatsAndRandomFailures(t *testing.T) {
	originalRead := randomRead
	defer func() { randomRead = originalRead; resetObjectIDStateForTest() }()
	resetObjectIDStateForTest()
	boom := errors.New("random unavailable")
	randomRead = func([]byte) (int, error) { return 0, boom }
	for _, generator := range []string{"uuidv1", "uuidv4", "uuidv7", "objectid"} {
		if _, err := GenerateID(generator); !errors.Is(err, boom) {
			t.Errorf("%s error = %v", generator, err)
		}
	}
	if _, err := GenerateID("serial"); err == nil {
		t.Fatal("serial must use NextSerial")
	}

	randomRead = originalRead
	resetObjectIDStateForTest()
	for _, generator := range []struct {
		name    string
		version byte
	}{{"uuidv1", 1}, {"uuidv4", 4}, {"uuidv7", 7}} {
		id, err := GenerateID(generator.name)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(id, "-")
		if len(id) != 36 || len(parts) != 5 || len(parts[0]) != 8 || len(parts[4]) != 12 {
			t.Fatalf("invalid UUID format %q", id)
		}
		version, _ := strconv.ParseUint(parts[2][:1], 16, 4)
		if byte(version) != generator.version {
			t.Errorf("%s version nibble is %x", id, version)
		}
		variant, _ := strconv.ParseUint(parts[3][:1], 16, 4)
		if variant < 8 || variant > 11 {
			t.Errorf("invalid UUID variant in %q", id)
		}
		if generator.name == "uuidv1" {
			node, _ := strconv.ParseUint(parts[4][:2], 16, 8)
			if node&1 == 0 {
				t.Errorf("v1 node is not marked random: %q", id)
			}
		}
		if generator.name == "uuidv7" {
			millis, err := strconv.ParseUint(strings.Join(parts[:3], "")[:12], 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			now := uint64(time.Now().UnixMilli())
			if millis > now || now-millis > 10000 {
				t.Fatalf("v7 timestamp %d is not current", millis)
			}
		}
	}
	first, err := GenerateID("objectid")
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateID("OBJECTID")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 24 || len(second) != 24 {
		t.Fatalf("invalid ObjectID sizes: %q %q", first, second)
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("ObjectID is not hex: %v", err)
	}
	if first[:18] != second[:18] {
		t.Fatalf("ObjectID process portion changed: %s %s", first, second)
	}
	n1, _ := strconv.ParseUint(first[18:], 16, 24)
	n2, _ := strconv.ParseUint(second[18:], 16, 24)
	if n2 != n1+1 {
		t.Fatalf("ObjectID counters %d then %d", n1, n2)
	}
}
