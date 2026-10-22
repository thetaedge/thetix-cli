package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query":          map[string]any{"type": "string"},
		"days":           map[string]any{"type": "integer"},
		"strike":         map[string]any{"type": "number"},
		"live":           map[string]any{"type": "boolean"},
		"card_ids":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"details":        map[string]any{"type": "object"},
		"risk_level":     map[string]any{"type": "string", "enum": []any{"conservative", "aggressive"}},
		"content_base64": map[string]any{"type": "string", "contentEncoding": "base64"},
		"filename":       map[string]any{"type": "string"},
		"content_type":   map[string]any{"type": "string"},
	},
	"required": []any{"query"},
}

func TestConvert(t *testing.T) {
	schema, err := ParseSchema(testSchema)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name, value string, want any) {
		t.Helper()
		got, err := schema.Convert(name, value, strings.NewReader(""))
		if err != nil {
			t.Fatalf("Convert(%s, %q): %v", name, value, err)
		}
		if fmtAny(got) != fmtAny(want) {
			t.Fatalf("Convert(%s, %q) = %#v, want %#v", name, value, got, want)
		}
	}
	check("days", "7", int64(7))
	check("strike", "182.5", 182.5)
	check("live", "false", false)
	check("card_ids", "a, b,,c", []any{"a", "b", "c"})
	check("details", `{"strike":180}`, map[string]any{"strike": float64(180)})
	check("risk_level", "aggressive", "aggressive")
	for name, bad := range map[string]string{"days": "seven", "strike": "x", "risk_level": "wild", "details": "{"} {
		if _, err := schema.Convert(name, bad, strings.NewReader("")); err == nil {
			t.Errorf("Convert(%s, %q) accepted", name, bad)
		}
	}
}

func TestBase64FileArgument(t *testing.T) {
	schema, _ := ParseSchema(testSchema)
	path := filepath.Join(t.TempDir(), "statement.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := schema.Convert("content_base64", path, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if got != base64.StdEncoding.EncodeToString([]byte("%PDF-1.7")) {
		t.Fatalf("file not base64-encoded: %v", got)
	}
	args := map[string]any{"content_base64": got}
	schema.FillFileFields(args, map[string]string{"content_base64": path})
	if args["filename"] != "statement.pdf" || args["content_type"] != "application/pdf" {
		t.Fatalf("file fields not filled: %v", args)
	}
	fromStdin, _ := schema.Convert("content_base64", "-", strings.NewReader("hi"))
	if fromStdin != base64.StdEncoding.EncodeToString([]byte("hi")) {
		t.Fatalf("stdin not read: %v", fromStdin)
	}
}

func TestMissingAndNames(t *testing.T) {
	schema, _ := ParseSchema(testSchema)
	if missing := schema.Missing(map[string]any{}); len(missing) != 1 || missing[0] != "query" {
		t.Fatalf("Missing = %v", missing)
	}
	if FlagName("chat_id") != "chat-id" || CommandName("ask_thetix") != "ask-thetix" {
		t.Fatal("name mapping")
	}
	if _, err := ParseSchema(map[string]any{"type": "object", "required": []any{"nope"}}); err == nil {
		t.Fatal("undeclared required property accepted")
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string][]string{
		"ask":    {"--server", "http://localhost:3200", "ask", "hi"},
		"tools":  {"--json", "tools"},
		"status": {"--server=x", "status"},
		"":       {"--json"},
	}
	for want, args := range cases {
		if got := commandName(args); got != want {
			t.Errorf("commandName(%v) = %q, want %q", args, got, want)
		}
	}
}

func TestConstraints(t *testing.T) {
	schema, err := ParseSchema(map[string]any{"type": "object", "properties": map[string]any{
		"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 290},
		"price":        map[string]any{"type": "number", "minimum": 0.01},
		"chat_id":      map[string]any{"type": "string", "pattern": `^[0-9a-f]{8}-[0-9a-f]{4}$`},
		"approval_id":  map[string]any{"type": "string", "minLength": 16, "maxLength": 20},
		"ids":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": `^[a-z]+$`}},
		"amounts":      map[string]any{"type": "array", "items": map[string]any{"type": "number"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{"wait_seconds": "290", "price": "0.5", "chat_id": "0123abcd-ef01", "approval_id": "abcdefghijklmnop",
		"ids": "a,b", "amounts": "1.5, 2"}
	for name, value := range ok {
		if _, err := schema.Convert(name, value, strings.NewReader("")); err != nil {
			t.Errorf("Convert(%s, %q): %v", name, value, err)
		}
	}
	bad := map[string]string{"wait_seconds": "291", "price": "0", "chat_id": "../api-keys", "approval_id": "short",
		"ids": "a,B", "amounts": "1,x"}
	for name, value := range bad {
		if _, err := schema.Convert(name, value, strings.NewReader("")); err == nil {
			t.Errorf("Convert(%s, %q) accepted a value the server refuses", name, value)
		}
	}
	if got, _ := schema.Convert("amounts", "[1, 2.5]", strings.NewReader("")); fmtAny(got) != "[1,2.5]" {
		t.Errorf("a JSON array for a number list = %s", fmtAny(got))
	}
}
