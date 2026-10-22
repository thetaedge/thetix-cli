package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exit codes. Stable: scripts and agents branch on them.
const (
	ExitOK        = 0
	ExitToolError = 1  // the tool ran and reported a failure
	ExitUsage     = 2  // bad command line
	ExitAuth      = 3  // not logged in, or the credential was rejected
	ExitNetwork   = 4  // could not reach the server, or it broke the protocol
	ExitRefused   = 5  // a destructive tool without --yes
	ExitApproval  = 6  // waiting for the person in Thetix (an approval, the trial, a brokerage); rerun with the id
	ExitScope     = 7  // the connection lacks the permission the tool needs
	ExitPlan      = 8  // the person's plan doesn't include this; the message says what does
	ExitBug       = 70 // a contract violation: a bug in thetix
)

// connectPath starts every page the person finishes something on: an
// approval (/connect/approve/), the free trial (/connect/trial/), a
// brokerage (/connect/broker/).
const connectPath = "/connect/"

// structured is a result's structuredContent as an object (nil otherwise).
func structured(result *mcp.CallToolResult) map[string]any {
	m, _ := result.StructuredContent.(map[string]any)
	return m
}

// pendingApproval is the approval a result still waits for: its id and link,
// when the tool returned without the person's decision.
func pendingApproval(result *mcp.CallToolResult) (id, link string, ok bool) {
	s := structured(result)
	if s == nil || s["approval_required"] != true || result.IsError {
		return "", "", false
	}
	id, _ = s["approval_id"].(string)
	link, _ = s["approval_url"].(string)
	return id, link, id != ""
}

// missingScope is the permission a failed result says the connection lacks
// ("" when it failed for another reason). Both shapes the server sends: the
// MCP layer's {error: {code, required_scope}} and the API's
// {error: {code, details: {required_scope}}}.
func missingScope(result *mcp.CallToolResult) (string, bool) {
	s := structured(result)
	if s == nil || !result.IsError {
		return "", false
	}
	e, _ := s["error"].(map[string]any)
	if e == nil || e["code"] != "INSUFFICIENT_SCOPE" {
		return "", false
	}
	scope, _ := e["required_scope"].(string)
	if details, ok := e["details"].(map[string]any); ok && scope == "" {
		scope, _ = details["required_scope"].(string)
	}
	return scope, true
}

// pendingStep is the person step a result waits for (the free trial's
// checkout, a brokerage sign-in): its id and link, while it is pending.
func pendingStep(result *mcp.CallToolResult) (id, link string, ok bool) {
	s := structured(result)
	if s == nil || s["step_required"] != true || s["status"] != "pending" || result.IsError {
		return "", "", false
	}
	id, _ = s["step_id"].(string)
	link, _ = s["step_url"].(string)
	return id, link, id != ""
}

// planAction is what lifts a plan refusal (the server's 402): start_trial,
// reactivate, wait_for_reset or upgrade; "" when the result is not one.
func planAction(result *mcp.CallToolResult) string {
	s := structured(result)
	if s == nil || !result.IsError {
		return ""
	}
	e, _ := s["error"].(map[string]any)
	if e == nil {
		return ""
	}
	action, _ := e["action"].(string)
	return action
}

// personLink finds a /connect/ page link in a progress message.
func personLink(message string) string {
	for _, field := range strings.Fields(message) {
		if strings.Contains(field, connectPath) && (strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://")) {
			return strings.TrimRight(field, ".,;)")
		}
	}
	return ""
}

// linkKind is the kind of /connect/ page a link opens: approve, trial,
// broker, or "" for anything else.
func linkKind(link string) string {
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(u.Path, connectPath) {
		return ""
	}
	kind := strings.SplitN(strings.TrimPrefix(u.Path, connectPath), "/", 2)[0]
	switch kind {
	case "approve", "trial", "broker":
		return kind
	}
	return ""
}

// linkOnServer reports whether a link is a Thetix page of the server the
// CLI talks to: over https on the server's host or a domain it is under
// (api.thetix.ai's pages are on thetix.ai), or (local development) any
// loopback host when the server is on one. The CLI opens nothing else.
func linkOnServer(link, server string) bool {
	l, err := url.Parse(link)
	if err != nil || linkKind(link) == "" {
		return false
	}
	s, err := url.Parse(server)
	if err != nil {
		return false
	}
	if isLoopback(s.Hostname()) {
		return isLoopback(l.Hostname()) && (l.Scheme == "http" || l.Scheme == "https")
	}
	bare := func(host string) string { return strings.TrimPrefix(strings.ToLower(host), "www.") }
	link, api := bare(l.Hostname()), bare(s.Hostname())
	return l.Scheme == "https" && link != "" && (link == api || strings.HasSuffix(api, "."+link))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// exitError carries a message and the exit code for it.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// isTerminal reports whether w is an interactive terminal.
func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// prettyText indents text that is a JSON document; other text is unchanged.
func prettyText(text string) string {
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
		var out bytes.Buffer
		if json.Indent(&out, []byte(trimmed), "", "  ") == nil {
			return out.String()
		}
	}
	return text
}

// printResult writes a tool result: its text (failures to errOut), or with
// asJSON the whole MCP result. Returns the exit code for it.
func printResult(out, errOut io.Writer, result *mcp.CallToolResult, asJSON bool) int {
	code := ExitOK
	if result.IsError {
		code = ExitToolError
	}
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(result)
		return code
	}
	w := out
	if result.IsError {
		w = errOut
	}
	for _, content := range result.Content {
		switch c := content.(type) {
		case *mcp.TextContent:
			fmt.Fprintln(w, prettyText(c.Text))
		case *mcp.ImageContent:
			fmt.Fprintf(w, "[image: %s, %d bytes]\n", c.MIMEType, len(c.Data))
		case *mcp.ResourceLink:
			fmt.Fprintf(w, "[resource: %s]\n", c.URI)
		default:
			fmt.Fprintf(w, "[%T]\n", c)
		}
	}
	return code
}
