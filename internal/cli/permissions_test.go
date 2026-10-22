package cli

import (
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

const approvalURL = "https://thetix.ai/connect/approve/abcdefghijklmnopqrstuvwx"

func meta(group, scope string) mcp.Meta {
	return mcp.Meta{"thetix/group": group, "thetix/scope": scope}
}

func text(s string) []mcp.Content { return []mcp.Content{&mcp.TextContent{Text: s}} }

// fakeAgentThetix is a Thetix-shaped server with grouped tools, object
// arguments, an approval-gated change and a tool the connection may not use.
func fakeAgentThetix(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	calls := &[]map[string]any{}
	server := mcp.NewServer(&mcp.Implementation{Name: "thetix", Version: "test"}, nil)
	record := func(name string, req *mcp.CallToolRequest) map[string]any {
		args := map[string]any{}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		*calls = append(*calls, map[string]any{"tool": name, "args": args})
		return args
	}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	server.AddTool(&mcp.Tool{Name: "list_chats", Title: "List chats", Meta: meta("chats", "read"), Annotations: readOnly,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			record("list_chats", req)
			return &mcp.CallToolResult{Content: text("[]")}, nil
		})
	server.AddTool(&mcp.Tool{Name: "stage_trade", Title: "Stage a trade", Meta: meta("trading", "trade"),
		InputSchema: map[string]any{"type": "object", "required": []any{"trade_spec"}, "properties": map[string]any{
			"trade_spec": map[string]any{"type": "object"},
			"legs":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"quantities": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"account_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{8}$"},
			"timeout":    map[string]any{"type": "integer"}}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			record("stage_trade", req)
			return &mcp.CallToolResult{Content: text("staged")}, nil
		})
	server.AddTool(&mcp.Tool{Name: "invite_member", Title: "Invite a member", Meta: meta("workspace", "admin"),
		InputSchema: map[string]any{"type": "object", "required": []any{"email"}, "properties": map[string]any{
			"email":       map[string]any{"type": "string", "minLength": 1},
			"approval_id": map[string]any{"type": "string", "minLength": 16, "maxLength": 64}}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := record("invite_member", req)
			switch args["approval_id"] {
			case "abcdefghijklmnopqrstuvwx":
				return &mcp.CallToolResult{Content: text("Invited " + args["email"].(string))}, nil
			case "deniedxxxxxxxxxxxxxxxxx":
				return &mcp.CallToolResult{IsError: true, Content: text("The person declined this in Thetix."),
					StructuredContent: map[string]any{"approval_required": true, "status": "denied"}}, nil
			}
			if token := req.Params.GetProgressToken(); token != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 1,
					Message: "Waiting for the person to approve this in Thetix: " + approvalURL})
			}
			return &mcp.CallToolResult{Content: text("Not done yet: the person must approve this in Thetix first."),
				StructuredContent: map[string]any{"approval_required": true, "approval_id": "abcdefghijklmnopqrstuvwx",
					"approval_url": approvalURL, "summary": "Invite x@example.com"}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "cancel_order", Title: "Cancel an order", Meta: meta("trading", "trade"),
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: text("This connection does not have the 'trade' permission."),
				StructuredContent: map[string]any{"error": map[string]any{"status": 403, "code": "INSUFFICIENT_SCOPE",
					"required_scope": "trade"}}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "legacy_tool", Title: "A tool without meta",
		InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: text("ok")}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, calls
}

func loginAgent(t *testing.T) *[]map[string]any {
	t.Helper()
	isolate(t)
	ts, calls := fakeAgentThetix(t)
	if code, out, errOut := cli(t, goodToken+"\n", "login", "--server", ts.URL+"/mcp", "--api-key", "-"); code != ExitOK {
		t.Fatalf("login: exit %d %q %q", code, out, errOut)
	} else if !strings.Contains(out, "Permissions: read, trade, admin") {
		t.Fatalf("login shows no permissions: %q", out)
	}
	return calls
}

func lastArgs(calls *[]map[string]any) map[string]any {
	return (*calls)[len(*calls)-1]["args"].(map[string]any)
}

func TestHelpGroupsToolsByServerGroup(t *testing.T) {
	loginAgent(t)
	code, out, _ := cli(t, "", "--help")
	if code != ExitOK {
		t.Fatalf("--help: exit %d", code)
	}
	order := []string{"Chats:", "list-chats", "Trading (cart and orders):", "stage-trade", "Workspace and API keys:",
		"invite-member", "Thetix tools:", "legacy-tool", "CLI commands:", "login"}
	at := 0
	for _, want := range order {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("--help lacks %q after position %d:\n%s", want, at, out)
		}
		at += i
	}
	if strings.Contains(out, "Market data:") {
		t.Fatalf("an empty group is listed:\n%s", out)
	}
	code, out, _ = cli(t, "", "tools")
	if code != ExitOK || !strings.Contains(out, "Trading (cart and orders):\n  cancel-order") ||
		!strings.Contains(out, "(admin)") || !strings.Contains(out, "permissions: read, trade, admin") {
		t.Fatalf("tools: exit %d\n%s", code, out)
	}
	if code, out, _ = cli(t, "", "status"); code != ExitOK || !strings.Contains(out, "Permissions: read, trade, admin") {
		t.Fatalf("status: exit %d\n%s", code, out)
	}
}

func TestObjectAndListArguments(t *testing.T) {
	calls := loginAgent(t)
	legs := filepath.Join(t.TempDir(), "legs.json")
	if err := os.WriteFile(legs, []byte(`[{"side":"sell","qty":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := cli(t, "", "stage-trade", `{"symbol":"AAPL"}`, "--legs", "@"+legs, "--quantities", "1, 2",
		"--arg-timeout", "30", "--yes")
	if code != ExitOK {
		t.Fatalf("stage-trade: exit %d %q", code, errOut)
	}
	args := lastArgs(calls)
	if fmtAny(args["trade_spec"]) != `{"symbol":"AAPL"}` || fmtAny(args["legs"]) != `[{"qty":1,"side":"sell"}]` ||
		fmtAny(args["quantities"]) != `[1,2]` || args["timeout"] != float64(30) {
		t.Fatalf("arguments sent: %s", fmtAny(args))
	}
	// The whole arguments object from standard input.
	if code, _, errOut := cli(t, `{"trade_spec":{"symbol":"MSFT"},"quantities":[3]}`, "stage-trade", "--args", "-"); code != ExitOK {
		t.Fatalf("--args -: exit %d %q", code, errOut)
	}
	if fmtAny(lastArgs(calls)["trade_spec"]) != `{"symbol":"MSFT"}` {
		t.Fatalf("--args - sent %s", fmtAny(lastArgs(calls)))
	}
	before := len(*calls)
	for _, bad := range [][]string{
		{"stage-trade", "{not json"},
		{"stage-trade", "{}", "--account-id", "nothex!!"},
		{"stage-trade", "{}", "--quantities", "1,two"},
		{"list-chats", "--limit", "0"},
		{"invite-member", "  "},
	} {
		if code, _, errOut := cli(t, "", bad...); code != ExitUsage {
			t.Errorf("%v: exit %d %q, want a usage error", bad, code, errOut)
		}
	}
	if len(*calls) != before {
		t.Fatalf("an invalid command reached the server")
	}
}

func TestApprovalPendingApprovedAndDenied(t *testing.T) {
	calls := loginAgent(t)
	code, out, errOut := cli(t, "", "invite-member", "x@example.com")
	if code != ExitApproval {
		t.Fatalf("invite-member: exit %d, want %d (%q %q)", code, ExitApproval, out, errOut)
	}
	if strings.Count(errOut, approvalURL) != 1 || !strings.Contains(errOut, "--approval-id abcdefghijklmnopqrstuvwx") {
		t.Fatalf("the approval link and rerun hint, once each: %q", errOut)
	}
	code, out, errOut = cli(t, "", "invite-member", "x@example.com", "--approval-id", "abcdefghijklmnopqrstuvwx")
	if code != ExitOK || !strings.Contains(out, "Invited x@example.com") {
		t.Fatalf("approved rerun: exit %d %q %q", code, out, errOut)
	}
	if lastArgs(calls)["approval_id"] != "abcdefghijklmnopqrstuvwx" {
		t.Fatalf("approval id not sent: %s", fmtAny(lastArgs(calls)))
	}
	if code, _, errOut = cli(t, "", "invite-member", "x@example.com", "--approval-id", "deniedxxxxxxxxxxxxxxxxx"); code != ExitToolError ||
		!strings.Contains(errOut, "declined") {
		t.Fatalf("denied: exit %d %q", code, errOut)
	}
	// --json still exits with the approval code.
	if code, out, _ = cli(t, "", "--json", "invite-member", "x@example.com"); code != ExitApproval || !strings.Contains(out, `"approval_id"`) {
		t.Fatalf("--json pending: exit %d %q", code, out)
	}
}

func TestMissingPermission(t *testing.T) {
	loginAgent(t)
	code, _, errOut := cli(t, "", "cancel-order")
	if code != ExitScope || !strings.Contains(errOut, "thetix login --allow trade") || !strings.Contains(errOut, "Profile > API Keys") {
		t.Fatalf("cancel-order: exit %d %q", code, errOut)
	}
}

func TestRequestedScopes(t *testing.T) {
	for _, c := range []struct {
		allow    []string
		readOnly bool
		want     string
	}{
		{nil, false, `["read","act"]`},
		{[]string{"trade"}, false, `["read","act","trade"]`},
		{[]string{"Admin", " trade "}, false, `["read","act","admin","trade"]`},
		{[]string{"all"}, false, `["read","act","read","act","trade","admin"]`},
		{nil, true, `["read"]`},
	} {
		got, err := requestedScopes(c.allow, c.readOnly)
		if err != nil || fmtAny(got) != c.want {
			t.Errorf("requestedScopes(%v, %v) = %s, %v; want %s", c.allow, c.readOnly, fmtAny(got), err, c.want)
		}
	}
	if _, err := requestedScopes([]string{"everything"}, false); err == nil {
		t.Error("an unknown permission is accepted")
	}
	if _, err := requestedScopes([]string{"trade"}, true); err == nil {
		t.Error("--read-only with --allow is accepted")
	}
	isolate(t)
	if code, _, errOut := cli(t, "", "login", "--api-key", goodToken, "--allow", "trade"); code != ExitUsage ||
		!strings.Contains(errOut, "Profile > API Keys") {
		t.Fatalf("--allow with a pasted key: exit %d %q", code, errOut)
	}
}

func TestPersonLinkInProgress(t *testing.T) {
	if got := personLink("Waiting for the person to approve this in Thetix: " + approvalURL); got != approvalURL {
		t.Fatalf("personLink = %q", got)
	}
	if got := personLink("Reading your positions https://thetix.ai/x"); got != "" {
		t.Fatalf("personLink found %q in ordinary progress", got)
	}
}
