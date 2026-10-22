package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const goodToken = "te_test_token"

// fakeThetix is an MCP server with a few Thetix-shaped tools behind a bearer check.
func fakeThetix(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	calls := &[]map[string]any{}
	server := mcp.NewServer(&mcp.Implementation{Name: "thetix", Version: "test"},
		&mcp.ServerOptions{Instructions: "Ask Thetix."})
	record := func(name string, req *mcp.CallToolRequest) map[string]any {
		args := map[string]any{}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		*calls = append(*calls, map[string]any{"tool": name, "args": args})
		return args
	}
	destructive := true
	server.AddTool(&mcp.Tool{Name: "ask_thetix", Title: "Ask Thetix", InputSchema: map[string]any{
		"type": "object", "required": []any{"query"},
		"properties": map[string]any{"query": map[string]any{"type": "string"}, "chat_id": map[string]any{"type": "string"},
			"wait_seconds": map[string]any{"type": "integer"}}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := record("ask_thetix", req)
			if token := req.Params.GetProgressToken(); token != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Message: "Reading", Progress: 1})
			}
			chat := "chat-1"
			if id, ok := args["chat_id"].(string); ok {
				chat = id
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Answer to " + args["query"].(string)}},
				StructuredContent: map[string]any{"chat_id": chat, "done": true}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "list_ideas", Title: "List ideas", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"days": map[string]any{"type": "integer"}}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			record("list_ideas", req)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"ideas":[]}`}}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "place_trades", Title: "Place staged trades", InputSchema: map[string]any{
		"type": "object", "required": []any{"chat_id"}, "properties": map[string]any{"chat_id": map[string]any{"type": "string"}}},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			record("place_trades", req)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "placed"}}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "fails", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Not found: chat"}}}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodToken {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://x/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, calls
}

func cli(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), "test", args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("THETIX_CONFIG_DIR", t.TempDir())
	t.Setenv("THETIX_CACHE_DIR", t.TempDir())
	for _, v := range []string{"THETIX_SERVER", "THETIX_API_KEY", "THETAEDGE_API_KEY", "THETAEDGE_API_BASE"} {
		t.Setenv(v, "")
	}
	// Approval links are printed, never opened, in tests.
	t.Setenv("THETIX_NO_BROWSER", "1")
}

func TestLoginToolsAndCalls(t *testing.T) {
	isolate(t)
	ts, calls := fakeThetix(t)
	server := ts.URL + "/mcp"

	if code, _, errOut := cli(t, "", "status", "--server", server); code != ExitAuth {
		t.Fatalf("status before login: exit %d (%s)", code, errOut)
	}
	if code, _, errOut := cli(t, "te_wrong\n", "login", "--server", server, "--api-key", "-"); code != ExitAuth {
		t.Fatalf("login with a bad key: exit %d (%s)", code, errOut)
	}
	if code, out, errOut := cli(t, goodToken+"\n", "login", "--server", server, "--api-key", "-"); code != ExitOK || !strings.Contains(out, "4 tools") {
		t.Fatalf("login: exit %d %q %q", code, out, errOut)
	}
	// The login made this server the default: no --server from here on.
	if code, out, _ := cli(t, "", "tools"); code != ExitOK || !strings.Contains(out, "list-ideas") || !strings.Contains(out, "(destructive)") {
		t.Fatalf("tools: exit %d %q", code, out)
	}
	if code, out, _ := cli(t, "", "list-ideas", "--days", "7"); code != ExitOK || !strings.Contains(out, `"ideas": []`) {
		t.Fatalf("list-ideas: exit %d %q", code, out)
	}
	if got := (*calls)[len(*calls)-1]["args"].(map[string]any)["days"]; got != float64(7) {
		t.Fatalf("days sent as %#v, want the integer 7", got)
	}
	if code, out, _ := cli(t, "", "ask", "How is AAPL?"); code != ExitOK || !strings.Contains(out, "Answer to How is AAPL?") {
		t.Fatalf("ask: exit %d %q", code, out)
	}
	if code, _, _ := cli(t, "", "ask", "--continue", "And MSFT?"); code != ExitOK {
		t.Fatalf("ask --continue: exit %d", code)
	}
	if got := (*calls)[len(*calls)-1]["args"].(map[string]any)["chat_id"]; got != "chat-1" {
		t.Fatalf("--continue sent chat_id %#v, want chat-1", got)
	}
	if code, out, _ := cli(t, "", "--json", "call", "list_ideas", "days=3"); code != ExitOK || !strings.Contains(out, `"content"`) {
		t.Fatalf("call --json: exit %d %q", code, out)
	}
	if code, _, errOut := cli(t, "", "place-trades", "chat-1"); code != ExitRefused || !strings.Contains(errOut, "--yes") {
		t.Fatalf("place-trades without --yes: exit %d %q", code, errOut)
	}
	before := len(*calls)
	if code, out, _ := cli(t, "", "place-trades", "chat-1", "--yes"); code != ExitOK || out != "placed\n" || len(*calls) != before+1 {
		t.Fatalf("place-trades --yes: exit %d %q", code, out)
	}
	if code, _, errOut := cli(t, "", "fails"); code != ExitToolError || !strings.Contains(errOut, "Not found") {
		t.Fatalf("a failing tool: exit %d %q", code, errOut)
	}
	if code, _, errOut := cli(t, "", "list-ideas", "--days", "seven"); code != ExitUsage || !strings.Contains(errOut, "whole number") {
		t.Fatalf("a bad integer: exit %d %q", code, errOut)
	}
	if code, _, _ := cli(t, "", "ask"); code != ExitUsage {
		t.Fatalf("ask without a question: exit %d", code)
	}
	if code, out, _ := cli(t, "", "logout"); code != ExitOK || !strings.Contains(out, "still active") {
		t.Fatalf("logout of an API key: exit %d %q", code, out)
	}
}

// The skills command, offline: install for the default agents and the ones
// detected, leave a foreign thetix skill alone, point out the retired
// thetix-skill clone, report versions, and uninstall only what it wrote.
func TestSkills(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("PATH", t.TempDir()) // no agent programs: detection is by directory only
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill := func(dir, body string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(home, ".claude", "skills", "thetix-skill")
	writeSkill(legacy, "---\nname: thetix\ndescription: the old skill\n---\n")

	code, out, errOut := cli(t, "", "skills", "install")
	if code != ExitOK {
		t.Fatalf("install: %d %s %s", code, out, errOut)
	}
	for _, rel := range []string{".claude/skills/thetix", ".agents/skills/thetix", ".config/opencode/skills/thetix"} {
		data, err := os.ReadFile(filepath.Join(home, rel, "SKILL.md"))
		if err != nil || !strings.Contains(string(data), `thetix_cli_version: "test"`) {
			t.Fatalf("%s: %v %q", rel, err, data)
		}
	}
	if !strings.Contains(errOut, legacy) {
		t.Fatalf("the retired thetix-skill clone is pointed out: %s", errOut)
	}
	if _, out, _ = cli(t, "", "skills", "install"); strings.Count(out, "up to date") != 3 {
		t.Fatalf("a second install changes nothing: %s", out)
	}

	foreign := filepath.Join(home, ".copilot", "skills", "thetix")
	writeSkill(foreign, "---\nname: thetix\ndescription: not ours\n---\n")
	code, out, _ = cli(t, "", "skills", "install", "--agents", "copilot")
	if code != ExitToolError || !strings.Contains(out, "a different skill named thetix") {
		t.Fatalf("a foreign thetix skill is refused: %d %s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(foreign, "SKILL.md")); !strings.Contains(string(data), "not ours") {
		t.Fatalf("the foreign skill is untouched: %q", data)
	}
	if code, _, errOut = cli(t, "", "skills", "install", "--agents", "vim"); code != ExitUsage ||
		!strings.Contains(errOut, "unknown agent") {
		t.Fatalf("unknown agent: %d %s", code, errOut)
	}

	_, out, _ = cli(t, "", "skills", "list", "--json")
	var listed []map[string]string
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 4 {
		t.Fatalf("list: %v %s", err, out)
	}
	if listed[0]["skill"] != "installed (test)" || listed[2]["default"] != "yes" || listed[3]["default"] != "yes" ||
		listed[3]["skill"] != "not installed" {
		t.Fatalf("list states: %s", out)
	}
	if _, out, _ = cli(t, "", "skills", "print"); !strings.HasPrefix(out, "---\nname: thetix\n") {
		t.Fatalf("print: %s", out[:40])
	}

	if code, out, _ = cli(t, "", "skills", "uninstall"); code != ExitOK || strings.Count(out, "removed") != 3 {
		t.Fatalf("uninstall: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(foreign, "SKILL.md")); err != nil {
		t.Fatalf("uninstall leaves the foreign skill: %v", err)
	}
}
