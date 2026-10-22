package cli

import "github.com/modelcontextprotocol/go-sdk/mcp"

// The server files each tool under a group and the permission it needs, in
// the tool's _meta.
const (
	metaGroup = "thetix/group"
	metaScope = "thetix/scope"
)

// toolGroups are the server's groups in display order, with their help titles.
var toolGroups = []struct{ ID, Title string }{
	{"plan", "Getting started and your plan:"},
	{"chats", "Chats:"},
	{"cards", "Cards and boards:"},
	{"portfolio", "Portfolio:"},
	{"market", "Market data:"},
	{"daily", "Daily edition, watchlist, community and alerts:"},
	{"ideas", "Ideas, opportunities and calculators:"},
	{"trading", "Trading (cart and orders):"},
	{"accounts", "Accounts and brokerage connections:"},
	{"documents", "Documents:"},
	{"profile", "Profile, memory and settings:"},
	{"workspace", "Workspace and API keys:"},
}

// Permissions, in the order the server grants them.
var allScopes = []string{"read", "act", "trade", "admin"}

func metaString(tool *mcp.Tool, key string) string {
	if tool == nil || tool.Meta == nil {
		return ""
	}
	value, _ := tool.Meta[key].(string)
	return value
}

// toolGroup is the command group for a tool: its server group when the CLI
// knows it, else the catch-all tools group.
func toolGroup(tool *mcp.Tool) string {
	group := metaString(tool, metaGroup)
	for _, g := range toolGroups {
		if g.ID == group {
			return group
		}
	}
	return groupTools
}

// toolScope is the permission a tool needs ("" when the server did not say).
func toolScope(tool *mcp.Tool) string { return metaString(tool, metaScope) }

// offeredScopes are the permissions a connection evidently has: those of the
// tools the server offers it (tools/list lists only what it may use). Empty
// when no tool says which permission it needs.
func offeredScopes(tools []*mcp.Tool) []string {
	seen := map[string]bool{}
	for _, tool := range tools {
		seen[toolScope(tool)] = true
	}
	var scopes []string
	for _, scope := range allScopes {
		if seen[scope] {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}
