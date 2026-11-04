package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/liberocks/dynamox/schema"
)

func TestServeValidatesFormatsAndReportsParseErrors(t *testing.T) {
	input := strings.Join([]string{
		`{"id":1,"method":"validate","text":"model Account {\n id Boolean @id\n @@table(name: \"accounts\")\n}"}`,
		`{"id":2,"method":"format","text":"model Account{\nid String @id\n@@table(name:\"accounts\")\n}"}`,
		`{"id":3,"method":"validate","text":"model Account {\n id String @id\n bad #\n}"}`,
		`{"id":4,"method":"format","text":"model Account { id String @id @@table(name: \"accounts\")"}`,
		`{"id":5,"method":"other"}`,
		`not-json`,
		`{"id":6,"method":"validate","text":"model Session {\n id String @id\n expiresAt String\n @@table(name: \"sessions\", read: 0, write: 1)\n @@ttl(field: expiresAt)\n}"}`,
		`{"id":7,"method":"format","text":"model Event{\nid String @id\nexpiresAt Int?\n@@table(name:\"events\",read:5,write:2)\n@@index(name:\"ByExpiry\",partition:expiresAt,type:GSI,read:2,write:1)\n@@ttl(field:expiresAt)\n}"}`,
		`{"id":8,"method":"validate","text":"` + strings.Repeat("x", schema.MaxSourceBytes+1) + `"}`,
	}, "\n")
	var output strings.Builder
	if err := serve(strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var responses []response
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var got response
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, got)
	}
	if len(responses) != 9 {
		t.Fatalf("got %d responses, want 9: %s", len(responses), output.String())
	}
	if responses[0].ID != 1 || len(responses[0].Diagnostics) == 0 || !strings.Contains(responses[0].Diagnostics[0].Message, "partition key") {
		t.Fatalf("validation response: %#v", responses[0])
	}
	if responses[1].ID != 2 || !strings.Contains(responses[1].Formatted, "model Account {") || len(responses[1].Diagnostics) != 0 {
		t.Fatalf("format response: %#v", responses[1])
	}
	if responses[2].ID != 3 || len(responses[2].Diagnostics) != 1 || responses[2].Diagnostics[0].Line != 3 || responses[2].Diagnostics[0].Column != 6 {
		t.Fatalf("parse diagnostic: %#v", responses[2])
	}
	if responses[3].ID != 4 || len(responses[3].Diagnostics) != 1 || responses[3].Diagnostics[0].Line != 1 {
		t.Fatalf("malformed format response: %#v", responses[3])
	}
	if responses[4].ID != 5 || responses[4].Error == "" {
		t.Fatalf("unknown method response: %#v", responses[4])
	}
	if !strings.Contains(responses[5].Error, "invalid request") {
		t.Fatalf("invalid JSON response: %#v", responses[5])
	}
	if responses[6].ID != 6 || len(responses[6].Diagnostics) < 2 || !strings.Contains(responses[6].Diagnostics[0].Message, "read capacity") || responses[6].Diagnostics[0].Line != 1 || responses[6].Diagnostics[0].Column != 7 || !strings.Contains(responses[6].Diagnostics[1].Message, "TTL field") || responses[6].Diagnostics[1].Line != 3 {
		t.Fatalf("TTL/capacity diagnostics: %#v", responses[6])
	}
	if responses[7].ID != 7 || len(responses[7].Diagnostics) != 0 || !strings.Contains(responses[7].Formatted, "read: 5, write: 2") || !strings.Contains(responses[7].Formatted, "@@ttl(field: expiresAt)") {
		t.Fatalf("TTL/capacity formatting: %#v", responses[7])
	}
	if responses[8].ID != 8 || !strings.Contains(responses[8].Error, "editor limit") {
		t.Fatalf("oversized request was not rejected: %#v", responses[8])
	}
}

func TestParseErrorFallbackAndLocations(t *testing.T) {
	for _, test := range []struct {
		input       string
		line, col   int
		wantMessage string
	}{
		{"2:7: unexpected character", 2, 7, "unexpected character"},
		{"plain error", 1, 1, "plain error"},
		{"x:3: bad position", 1, 1, "x:3: bad position"},
		{"0:2: bad position", 1, 1, "0:2: bad position"},
		{"2:", 1, 1, "2:"},
	} {
		line, col, message := parseError(test.input)
		if line != test.line || col != test.col || message != test.wantMessage {
			t.Errorf("parseError(%q) = (%d,%d,%q)", test.input, line, col, message)
		}
	}
}

func TestServePropagatesReadAndWriteErrors(t *testing.T) {
	readErr := errors.New("read failed")
	if err := serve(errorReader{err: readErr}, io.Discard); !errors.Is(err, readErr) {
		t.Fatalf("serve read error = %v", err)
	}
	writeErr := errors.New("write failed")
	if err := serve(strings.NewReader(`{"id":1,"method":"validate"}`), errorWriter{err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("serve write error = %v", err)
	}
	if err := serve(strings.NewReader(strings.Repeat("not-json\n", 3000)), errorWriter{err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("serve invalid-request write error = %v", err)
	}
	if err := serve(strings.NewReader(strings.Repeat(`{"method":"other"}`+"\n", 3000)), errorWriter{err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("serve method-response write error = %v", err)
	}
	largeComment := "//" + strings.Repeat("x", 70*1024) + "\nmodel A {\n id String @id\n @@table(name: \"a\")\n}"
	largeResponse := `{"method":"format","text":` + mustJSON(t, largeComment) + "}\n"
	if err := serve(strings.NewReader(largeResponse), errorWriter{err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("serve large-response write error = %v", err)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestMainUsesStandardStreams(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString(`{"id":9,"method":"validate","text":"model A { id String @id @@table(name: \"a\") }"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, output
	t.Cleanup(func() { os.Stdin, os.Stdout = oldStdin, oldStdout })
	main()
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got response
	if err := json.Unmarshal([]byte(line), &got); err != nil || got.ID != 9 {
		t.Fatalf("main response = %#v, error = %v", got, err)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }
