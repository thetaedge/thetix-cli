package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
