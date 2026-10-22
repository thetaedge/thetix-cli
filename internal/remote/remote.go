// Package remote is the CLI's session with a Thetix MCP server: connect with
// a bearer token, list tools, and call them with their progress reported.
package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thetaedge/thetix-cli/internal/contract"
)

// ErrUnauthorized means the server rejected (or needs) the credential.
var ErrUnauthorized = errors.New("not authorized")

// Progress receives a running call's status messages.
type Progress func(message string, progress float64)

// bearer adds the token to every request and remembers a 401.
type bearer struct {
	token        string
	base         http.RoundTripper
	unauthorized atomic.Bool
}

func (b *bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	resp, err := b.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		b.unauthorized.Store(true)
	}
	return resp, err
}

// Session is one connection to the server. Safe for concurrent calls.
type Session struct {
	Server string
	mcp    *mcp.ClientSession
	auth   *bearer
	calls  sync.Map // progress token (string) -> Progress
	next   atomic.Int64
}

// Connect opens a session. Pre: server is a normalized MCP URL.
func Connect(ctx context.Context, server, token, version string) (*Session, error) {
	contract.Require(server != "", "Connect needs a server URL")
	s := &Session{Server: server, auth: &bearer{token: token, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "thetix-cli", Title: "Thetix CLI", Version: version},
		&mcp.ClientOptions{ProgressNotificationHandler: s.onProgress})
	transport := &mcp.StreamableClientTransport{
		Endpoint: server,
		// No overall timeout: a tool call streams until Thetix answers; callers bound it with ctx.
		HTTPClient:           &http.Client{Transport: s.auth},
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, s.classify(err)
	}
	s.mcp = session
	return s, nil
}

func (s *Session) classify(err error) error {
	if s.auth.unauthorized.Load() {
		return fmt.Errorf("%w by %s: %v", ErrUnauthorized, s.Server, err)
	}
	return err
}

func (s *Session) onProgress(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
	token := fmt.Sprint(req.Params.ProgressToken)
	if listener, ok := s.calls.Load(token); ok {
		listener.(Progress)(req.Params.Message, req.Params.Progress)
	}
}

// Instructions are the server's guidance for the model.
func (s *Session) Instructions() string {
	if r := s.mcp.InitializeResult(); r != nil {
		return r.Instructions
	}
	return ""
}

// ServerInfo describes the server and the negotiated protocol revision.
func (s *Session) ServerInfo() (name, version, protocol string) {
	r := s.mcp.InitializeResult()
	if r == nil {
		return "", "", ""
	}
	if r.ServerInfo != nil {
		name, version = r.ServerInfo.Name, r.ServerInfo.Version
	}
	return name, version, r.ProtocolVersion
}

// Tools lists every tool the server offers.
func (s *Session) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for tool, err := range s.mcp.Tools(ctx, nil) {
		if err != nil {
			return nil, s.classify(err)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// Call runs a tool. progress (may be nil) receives its status messages.
// A tool's own failure is a result with IsError set, not an error.
func (s *Session) Call(ctx context.Context, name string, args map[string]any, progress Progress) (*mcp.CallToolResult, error) {
	contract.Require(name != "", "Call needs a tool name")
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	if progress != nil {
		token := "thetix-cli-" + strconv.FormatInt(s.next.Add(1), 10)
		s.calls.Store(token, progress)
		defer s.calls.Delete(token)
		params.SetProgressToken(token)
	}
	result, err := s.mcp.CallTool(ctx, params)
	if err != nil {
		return nil, s.classify(err)
	}
	contract.Ensure(result != nil, "a result for a successful call")
	return result, nil
}

// Close ends the session.
func (s *Session) Close() error { return s.mcp.Close() }
