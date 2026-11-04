// Command dynamox-editor implements the small line-based protocol used by the
// VS Code extension. Each input line is one JSON request and each output line
// is its JSON response.
package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/liberocks/dynamox/schema"
)

type request struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Text   string `json:"text"`
}

type editorDiagnostic struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

type response struct {
	ID          int                `json:"id"`
	Diagnostics []editorDiagnostic `json:"diagnostics,omitempty"`
	Formatted   string             `json:"formatted,omitempty"`
	Error       string             `json:"error,omitempty"`
}

func main() {
	_ = serve(os.Stdin, os.Stdout)
}

func serve(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	writer := bufio.NewWriter(output)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if err := writeResponse(writer, response{Error: "invalid request: " + err.Error()}); err != nil {
				return err
			}
			continue
		}
		res := handle(req)
		if err := writeResponse(writer, res); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return writer.Flush()
}

func writeResponse(writer *bufio.Writer, res response) error {
	return json.NewEncoder(writer).Encode(res)
}

func handle(req request) response {
	res := response{ID: req.ID}
	if req.Method != "validate" && req.Method != "format" {
		res.Error = "unknown method " + strconv.Quote(req.Method)
		return res
	}
	parsed, err := schema.Parse(req.Text)
	if err != nil {
		line, column, message := parseError(err.Error())
		res.Diagnostics = []editorDiagnostic{{Line: line, Column: column, Message: message}}
		return res
	}
	if req.Method == "validate" {
		for _, diagnostic := range schema.Validate(parsed) {
			res.Diagnostics = append(res.Diagnostics, editorDiagnostic{
				Line: diagnostic.Position.Line, Column: diagnostic.Position.Column, Message: diagnostic.Message,
			})
		}
		return res
	}
	// Parse already lexed this source, so formatting cannot fail on its tokens.
	res.Formatted, _ = schema.Format(req.Text)
	return res
}

func parseError(message string) (int, int, string) {
	first, rest, ok := strings.Cut(message, ":")
	if !ok {
		return 1, 1, message
	}
	second, detail, ok := strings.Cut(rest, ":")
	if !ok {
		return 1, 1, message
	}
	line, lineErr := strconv.Atoi(first)
	column, columnErr := strconv.Atoi(second)
	if lineErr != nil || columnErr != nil || line < 1 || column < 1 {
		return 1, 1, message
	}
	return line, column, strings.TrimSpace(detail)
}
