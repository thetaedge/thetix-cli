// Package proxy serves the remote Thetix tools as a local stdio MCP server,
// for MCP clients that launch a command instead of connecting to a URL.
// Each call is forwarded to the remote session; its progress notifications
// are forwarded back to the local client under the client's own token.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thetaedge/thetix-cli/internal/contract"
	"github.com/thetaedge/thetix-cli/internal/remote"
)

// Serve mirrors the remote tools over stdio until the client disconnects or ctx ends.
func Serve(ctx context.Context, session *remote.Session, version string) error {
	contract.Require(session != nil, "Serve needs a connected session")
	tools, err := session.Tools(ctx)
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "thetix", Title: "Thetix", Version: version},
		&mcp.ServerOptions{Instructions: session.Instructions()})
	for _, tool := range tools {
		server.AddTool(tool, forward(session, tool.Name))
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

func forward(session *remote.Session, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return errorResult(fmt.Sprintf("arguments must be a JSON object: %v", err)), nil
			}
		}
		var progress remote.Progress
		if token := req.Params.GetProgressToken(); token != nil {
			progress = func(message string, value float64) {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token, Message: message, Progress: value})
			}
		}
		result, err := session.Call(ctx, name, args, progress)
		if err != nil {
			return errorResult(fmt.Sprintf("Thetix could not be reached: %v", err)), nil
		}
		return result, nil
	}
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
