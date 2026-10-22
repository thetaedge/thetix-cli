package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/thetaedge/thetix-cli/internal/auth"
	"github.com/thetaedge/thetix-cli/internal/config"
	"github.com/thetaedge/thetix-cli/internal/contract"
	"github.com/thetaedge/thetix-cli/internal/proxy"
	"github.com/thetaedge/thetix-cli/internal/remote"
)

func clientName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "Thetix CLI"
	}
	return "Thetix CLI on " + strings.Split(host, ".")[0]
}

func maskToken(token string) string {
	if len(token) <= 10 {
		return "****"
	}
	return token[:7] + "…" + token[len(token)-4:]
}

// requestedScopes are the permissions `thetix login` asks for: read and act,
// plus --allow's (comma-separated; "all" is every permission), or only read
// with --read-only.
func requestedScopes(allow []string, readOnly bool) ([]string, error) {
	if readOnly && len(allow) > 0 {
		return nil, fail(ExitUsage, "use --read-only or --allow, not both")
	}
	if readOnly {
		return []string{"read"}, nil
	}
	scopes := append([]string{}, auth.DefaultScopes...)
	for _, name := range allow {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "":
		case name == "all":
			scopes = append(scopes, auth.Permissions...)
		case auth.IsPermission(name):
			scopes = append(scopes, name)
		default:
			return nil, fail(ExitUsage, "--allow takes permissions from %s (or all), not %q",
				strings.Join(auth.Permissions, ", "), name)
		}
	}
	contract.Ensure(len(scopes) >= 2 && scopes[0] == "read", "a login asks for read and act at least")
	return scopes, nil
}

func (a *app) loginCommand() *cobra.Command {
	var apiKey string
	var allow []string
	var readOnly, device bool
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Connect thetix to your Thetix account",
		Long: "Connect thetix to your Thetix account. By default this opens Thetix in your browser to approve the\n" +
			"CLI (new to Thetix? create your account on that page); the CLI then gets its own API key, named after\n" +
			"this computer, which you can revoke in\n" +
			"Profile > Agent Access. With --api-key, thetix stores a key you created there instead (use - to read it\n" +
			"from standard input). The server you log in to becomes the default.\n\n" +
			"Permissions: a browser login asks for read (your accounts, market data, ideas, chats) and act\n" +
			"(ask Thetix, change chats, cards, watchlist and settings). --allow trade adds staging trades,\n" +
			"placing staged trades and cancelling orders; --allow admin adds the workspace, accounts,\n" +
			"brokerage connections and API keys (irreversible changes still need your approval in Thetix).\n" +
			"You decide on the consent page, and can change them later in Profile > Agent Access.\n\n" +
			"No browser on this machine (SSH, a server)? --device shows a code to enter at thetix.ai/connect/device\n" +
			"on any device instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := a.credentials()
			if err != nil {
				return err
			}
			scopes, err := requestedScopes(allow, readOnly)
			if err != nil {
				return err
			}
			method := config.AuthOAuth
			granted := ""
			token := strings.TrimSpace(apiKey)
			if token == "-" {
				data, err := io.ReadAll(a.stdin)
				if err != nil {
					return fail(ExitUsage, "reading the API key: %v", err)
				}
				token = strings.TrimSpace(string(data))
			}
			if token != "" {
				if !strings.HasPrefix(token, "te_") {
					return fail(ExitUsage, "a Thetix API key starts with te_ (create one in Profile > Agent Access, under API keys)")
				}
				if len(allow) > 0 || readOnly || device {
					return fail(ExitUsage, "a pasted API key has the permissions it was created with; "+
						"change them in Profile > Agent Access (--allow, --read-only and --device are for a sign-in)")
				}
				method = config.AuthAPIKey
			} else {
				ctx, cancel := context.WithTimeout(cmd.Context(), wait+time.Minute)
				defer cancel()
				issued, err := auth.Login(ctx, &http.Client{Timeout: time.Minute}, auth.LoginOptions{
					Server: creds.Server, ClientName: clientName(), OpenBrowser: !a.noBrowser && !device, Out: a.stderr,
					Timeout: wait, Scopes: scopes, Device: device,
				})
				if err != nil {
					return fail(ExitAuth, "%v", err)
				}
				token, granted = issued.AccessToken, issued.Scope
			}
			creds.Token = token

			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			session, err := remote.Connect(ctx, creds.Server, token, a.version)
			if err != nil {
				return connectionError(creds.Server, err)
			}
			defer session.Close()
			tools, err := session.Tools(ctx)
			if err != nil {
				return connectionError(creds.Server, err)
			}
			saveTools(creds, tools)

			account := a.cfg.Accounts[creds.Server]
			if account == nil {
				account = &config.Account{}
				a.cfg.Accounts[creds.Server] = account
			}
			if account.Token != "" && account.Token != token {
				forgetTools(config.Credentials{Server: creds.Server, Token: account.Token})
			}
			account.Token, account.Auth, account.Scope = token, method, granted
			a.cfg.Default = creds.Server
			if creds.Server == config.DefaultServer {
				a.cfg.Default = ""
			}
			if err := a.cfg.Save(); err != nil {
				return fail(ExitUsage, "saving %s: %v", a.cfg.Path(), err)
			}
			contract.Ensure(a.cfg.Accounts[creds.Server].Token == token, "the login is stored")
			fmt.Fprintf(a.stdout, "Logged in to %s (%d tools).\n", creds.Server, len(tools))
			if permissions := permissionsText(granted, tools); permissions != "" {
				fmt.Fprintf(a.stdout, "Permissions: %s\n", permissions)
			}
			fmt.Fprintln(a.stdout, `Next: thetix get-started (your plan, accounts and what to set up), or thetix ask "How is my portfolio doing?"`)
			if os.Getenv("THETIX_API_KEY") != "" {
				fmt.Fprintln(a.stderr, "Note: THETIX_API_KEY is set and takes precedence over this login.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "store this API key instead of signing in with the browser (- reads stdin)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "also ask for these permissions: trade, admin, or all (comma-separated)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "ask only for the read permission")
	cmd.Flags().BoolVar(&device, "device", false, "sign in with a code entered on any device (no browser on this machine)")
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "how long to wait for the sign-in")
	return cmd
}

// permissionsText describes a connection's permissions: those its tools
// need (the server lists only what the connection may use), else the scope
// granted at login, else "".
func permissionsText(granted string, tools []*mcp.Tool) string {
	if offered := offeredScopes(tools); len(offered) > 0 {
		return strings.Join(offered, ", ")
	}
	if granted != "" {
		return strings.Join(strings.Fields(granted), ", ") + " (granted at login)"
	}
	return ""
}

func (a *app) logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored login (and revoke the key a browser login created)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := a.credentials()
			if err != nil {
				return err
			}
			account := a.cfg.Accounts[creds.Server]
			if account == nil || account.Token == "" {
				fmt.Fprintf(a.stdout, "Not logged in to %s.\n", creds.Server)
				return nil
			}
			if account.Auth == config.AuthOAuth {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				defer cancel()
				if err := auth.Revoke(ctx, &http.Client{Timeout: 30 * time.Second}, creds.Server, account.Token); err != nil {
					fmt.Fprintf(a.stderr, "thetix: could not revoke the key (%v); revoke it in Profile > Agent Access.\n", err)
				}
			}
			delete(a.cfg.Accounts, creds.Server)
			if a.cfg.Default == creds.Server {
				a.cfg.Default = ""
			}
			if err := a.cfg.Save(); err != nil {
				return fail(ExitUsage, "saving %s: %v", a.cfg.Path(), err)
			}
			forgetTools(config.Credentials{Server: creds.Server, Token: account.Token})
			fmt.Fprintf(a.stdout, "Logged out of %s.\n", creds.Server)
			if account.Auth == config.AuthAPIKey {
				fmt.Fprintln(a.stdout, "The API key itself is still active; revoke it in Profile > Agent Access if you no longer need it.")
			}
			return nil
		},
	}
}

func (a *app) statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the server, the credential in use, and whether it works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := a.credentials()
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Server:     %s\n", creds.Server)
			if creds.Token == "" {
				fmt.Fprintln(a.stdout, "Credential: none")
				return fail(ExitAuth, "not logged in; run `thetix login`")
			}
			fmt.Fprintf(a.stdout, "Credential: %s (from %s)\n", maskToken(creds.Token), creds.Source)
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			started := time.Now()
			session, err := remote.Connect(ctx, creds.Server, creds.Token, a.version)
			if err != nil {
				return connectionError(creds.Server, err)
			}
			defer session.Close()
			tools, err := session.Tools(ctx)
			if err != nil {
				return connectionError(creds.Server, err)
			}
			saveTools(creds, tools)
			name, version, protocol := session.ServerInfo()
			fmt.Fprintf(a.stdout, "Connected:  %s %s, MCP %s, %d tools, %s\n", name, version, protocol, len(tools),
				time.Since(started).Round(time.Millisecond))
			granted := ""
			if account := a.cfg.Accounts[creds.Server]; account != nil && account.Token == creds.Token {
				granted = account.Scope
			}
			permissions := permissionsText(granted, tools)
			if permissions == "" {
				permissions = "unknown (the server does not say)"
			}
			fmt.Fprintf(a.stdout, "Permissions: %s\n", permissions)
			fmt.Fprintf(a.stdout, "Plan:       %s\n", planText(ctx, session))
			fmt.Fprintf(a.stdout, "Config:     %s\n", a.cfg.Path())
			return nil
		},
	}
}

func (a *app) mcpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve Thetix as a local stdio MCP server (for apps that launch MCP servers)",
		Long: "Serve the Thetix tools over stdio with the stored login, for MCP clients that launch local servers\n" +
			"rather than connecting to a URL. Configure the client to run `thetix mcp`. Progress from long\n" +
			"tools is forwarded. Logs go to standard error; standard output is the protocol.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			connectCtx, cancel := context.WithTimeout(ctx, time.Minute)
			session, creds, err := a.connect(connectCtx)
			cancel()
			if err != nil {
				return err
			}
			defer session.Close()
			if err := proxy.Serve(ctx, session, a.version); err != nil && ctx.Err() == nil {
				return connectionError(creds.Server, err)
			}
			return nil
		},
	}
}

// planText describes the person's plan from the get_plan_access tool (its
// result is /api/access as JSON), or says why it can't.
func planText(ctx context.Context, session *remote.Session) string {
	result, err := session.Call(ctx, "get_plan_access", map[string]any{}, nil)
	if err != nil || result.IsError || len(result.Content) == 0 {
		return "unknown (could not read it)"
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return "unknown"
	}
	var access struct {
		Plan     string `json:"plan"`
		DemoMode bool   `json:"demoMode"`
		Lapsed   bool   `json:"lapsed"`
	}
	if json.Unmarshal([]byte(text.Text), &access) != nil {
		return "unknown"
	}
	switch {
	case access.Plan == "edge":
		return "Edge"
	case access.Plan == "trial":
		return "Edge, free trial"
	case access.Lapsed:
		return "free (your trial or plan ended; `thetix start-free-trial` reactivates it)"
	case access.DemoMode:
		return "free, browsing Thetix's demo portfolio (`thetix start-free-trial` starts the trial)"
	default:
		return "free (`thetix start-free-trial` starts the trial)"
	}
}
