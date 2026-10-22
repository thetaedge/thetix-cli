// Package cli is the thetix command line: a front end of the Thetix MCP
// server. Every Thetix tool the server offers becomes a command
// (ask_thetix -> `thetix ask-thetix`, also `thetix ask`), its JSON Schema
// arguments become flags, and its required arguments may be given in order
// as positional arguments. The CLI adds login, status and a stdio MCP proxy
// for clients that can only launch local servers.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/thetaedge/thetix-cli/internal/auth"
	"github.com/thetaedge/thetix-cli/internal/config"
	"github.com/thetaedge/thetix-cli/internal/contract"
	"github.com/thetaedge/thetix-cli/internal/remote"
)

const (
	groupTools = "tools"
	groupCLI   = "cli"
	askTool    = "ask_thetix"
)

// Flags every command takes. A tool argument with one of these names gets
// the flag --arg-<name> instead.
var reservedFlags = map[string]bool{"server": true, "api-key": true, "json": true, "timeout": true,
	"no-progress": true, "no-browser": true, "no-wait": true, "yes": true, "args": true, "help": true,
	"version": true, "continue": true}

// flags that take a value, for finding the command name before parsing.
var valueFlags = map[string]bool{"--server": true, "--api-key": true, "--timeout": true}

type app struct {
	version        string
	stdin          io.Reader
	stdout, stderr io.Writer

	server, apiKey                     string
	asJSON                             bool
	noProgress, noBrowser, noWait, yes bool
	timeout                            time.Duration
	cfg                                *config.Config
}

// Main runs the command line and returns the process exit code.
func Main(version string, args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return Run(ctx, version, args, os.Stdin, os.Stdout, os.Stderr)
}

// Run is Main with its context and streams given.
func Run(ctx context.Context, version string, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			violation, ok := r.(contract.Violation)
			if !ok {
				panic(r)
			}
			fmt.Fprintf(stderr, "thetix: internal error, please report it: %v\n", violation)
			code = ExitBug
		}
	}()
	a := &app{version: version, stdin: stdin, stdout: stdout, stderr: stderr}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "thetix: %v\n", err)
		return ExitUsage
	}
	a.cfg = cfg

	root := a.rootCommand(ctx, args)
	root.SetArgs(args)
	err = root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var exit *exitError
	if errors.As(err, &exit) {
		if exit.err.Error() != "" {
			fmt.Fprintf(stderr, "thetix: %v\n", exit.err)
		}
		return exit.code
	}
	fmt.Fprintf(stderr, "thetix: %v\n", err)
	return ExitUsage
}

// commandName is the first argument that is not a flag (or a flag's value).
func commandName(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if valueFlags[arg] {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// flagValue is a value flag's value from raw args ("" when absent).
func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=")
		}
	}
	return ""
}

func (a *app) credentials() (config.Credentials, error) {
	creds, err := a.cfg.Resolve(a.server, a.apiKey)
	if err != nil {
		return creds, fail(ExitUsage, "%v", err)
	}
	return creds, nil
}

func (a *app) connect(ctx context.Context) (*remote.Session, config.Credentials, error) {
	creds, err := a.credentials()
	if err != nil {
		return nil, creds, err
	}
	if creds.Token == "" {
		return nil, creds, fail(ExitAuth, "not logged in to %s; run `thetix login`", creds.Server)
	}
	session, err := remote.Connect(ctx, creds.Server, creds.Token, a.version)
	if err != nil {
		return nil, creds, connectionError(creds.Server, err)
	}
	return session, creds, nil
}

func connectionError(server string, err error) error {
	if errors.Is(err, remote.ErrUnauthorized) {
		return fail(ExitAuth, "%s rejected the credential; run `thetix login` again", server)
	}
	return fail(ExitNetwork, "cannot talk to %s: %v", server, err)
}

// tools for the command tree: the cache, refreshed from the server when the
// command being run is not a known one.
func (a *app) tools(ctx context.Context, args []string, static map[string]bool) []*mcp.Tool {
	creds, err := a.cfg.Resolve(flagValue(args, "--server"), flagValue(args, "--api-key"))
	if err != nil {
		return nil
	}
	cached, fresh := loadTools(creds)
	name := commandName(args)
	known := static[name] || name == "" || name == "help" || name == "completion" || name == "__complete"
	if known || creds.Token == "" {
		return cached
	}
	if fresh && hasCommand(cached, name) {
		return cached
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := remote.Connect(fetchCtx, creds.Server, creds.Token, a.version)
	if err != nil {
		return cached
	}
	defer session.Close()
	tools, err := session.Tools(fetchCtx)
	if err != nil {
		return cached
	}
	saveTools(creds, tools)
	return tools
}

func hasCommand(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if CommandName(tool.Name) == name || (tool.Name == askTool && name == "ask") {
			return true
		}
	}
	return false
}

func (a *app) rootCommand(ctx context.Context, args []string) *cobra.Command {
	root := &cobra.Command{
		Use:   "thetix",
		Short: "Thetix, the options intelligence platform, from the command line",
		Long: "thetix runs Thetix's tools from your terminal or scripts: ask Thetix about your portfolio, read your\n" +
			"ideas, cards and trade cart, and price covered calls, puts and rolls. Every tool the Thetix MCP\n" +
			"server offers is a command; run `thetix login` first, then `thetix tools` to see them all.",
		Version:       a.version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetHelpCommandGroupID(groupCLI)
	root.SetCompletionCommandGroupID(groupCLI)

	persistent := root.PersistentFlags()
	persistent.StringVar(&a.server, "server", "", "Thetix MCP server URL (default "+config.DefaultServer+", or THETIX_SERVER)")
	persistent.StringVar(&a.apiKey, "api-key", "", "use this Thetix API key instead of the stored login (or THETIX_API_KEY)")
	persistent.BoolVar(&a.asJSON, "json", false, "print the full MCP result as JSON")
	persistent.BoolVar(&a.noProgress, "no-progress", false, "do not print progress while Thetix works")
	persistent.BoolVar(&a.yes, "yes", false, "run a destructive tool (such as place-trades) without asking")
	persistent.BoolVar(&a.noBrowser, "no-browser", false, "print sign-in, approval and other Thetix links instead of opening a browser")
	persistent.BoolVar(&a.noWait, "no-wait", false, "when a step waits for you in Thetix, print its link and id and exit 6 instead of waiting")
	persistent.DurationVar(&a.timeout, "timeout", 10*time.Minute, "give up on a tool call after this long")

	static := []*cobra.Command{a.loginCommand(), a.logoutCommand(), a.statusCommand(), a.toolsCommand(),
		a.callCommand(), a.mcpCommand(), a.versionCommand()}
	staticNames := map[string]bool{}
	for _, cmd := range static {
		staticNames[cmd.Name()] = true
	}
	var commands []*cobra.Command
	used := map[string]bool{}
	for _, tool := range a.tools(ctx, args, staticNames) {
		if staticNames[CommandName(tool.Name)] {
			continue
		}
		if cmd := a.toolCommand(tool, CommandName(tool.Name), nil); cmd != nil {
			commands = append(commands, cmd)
		}
		if tool.Name == askTool {
			if cmd := a.askCommand(tool); cmd != nil {
				commands = append(commands, cmd)
			}
		}
	}
	for _, cmd := range commands {
		used[cmd.GroupID] = true
	}
	// Groups appear in --help in the server's order, then other tools, then the CLI's own.
	for _, g := range toolGroups {
		if used[g.ID] {
			root.AddGroup(&cobra.Group{ID: g.ID, Title: g.Title})
		}
	}
	if used[groupTools] {
		root.AddGroup(&cobra.Group{ID: groupTools, Title: "Thetix tools:"})
	}
	root.AddGroup(&cobra.Group{ID: groupCLI, Title: "CLI commands:"})
	root.AddCommand(commands...)
	for _, cmd := range static {
		cmd.GroupID = groupCLI
		root.AddCommand(cmd)
	}
	return root
}

func describe(tool *mcp.Tool) (short, long string) {
	short = tool.Title
	if short == "" && tool.Annotations != nil {
		short = tool.Annotations.Title
	}
	if short == "" {
		short = strings.SplitN(tool.Description, "\n", 2)[0]
	}
	long = tool.Description
	if isDestructive(tool) {
		long += "\n\nDestructive: asks for confirmation, or pass --yes."
	}
	if scope := toolScope(tool); scope != "" && scope != "read" {
		long += fmt.Sprintf("\n\nNeeds the %q permission on this connection.", scope)
	}
	return short, long
}

// argFlag is a tool argument's flag name, kept clear of the CLI's own flags.
func argFlag(property string) string {
	flag := FlagName(property)
	if reservedFlags[flag] {
		flag = "arg-" + flag
	}
	contract.Ensure(!reservedFlags[flag], "tool argument flag %q is clear of the CLI's flags", flag)
	return flag
}

// parseArgsObject reads --args: a JSON object, @file.json, or - for standard input.
func parseArgsObject(raw string, stdin io.Reader) (map[string]any, error) {
	args := map[string]any{}
	if raw == "" {
		return args, nil
	}
	data := []byte(raw)
	if raw == "-" || strings.HasPrefix(raw, "@") {
		path := "-"
		if raw != "-" {
			path = raw[1:]
		}
		var err error
		if data, err = readFile(path, stdin); err != nil {
			return nil, fail(ExitUsage, "--args: %v", err)
		}
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return nil, fail(ExitUsage, "--args takes a JSON object (or @file.json, or - for standard input): %v", err)
	}
	return args, nil
}

func isDestructive(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint &&
		!tool.Annotations.ReadOnlyHint
}

// typedValue is a tool argument's flag: kept as text until the schema
// converts it, with its JSON type shown in --help.
type typedValue struct {
	text string
	kind string
}

func (v *typedValue) String() string     { return v.text }
func (v *typedValue) Set(s string) error { v.text = s; return nil }
func (v *typedValue) Type() string       { return v.kind }

// toolCommand is the command for one tool. before (may be nil) adjusts the
// arguments just before the call.
func (a *app) toolCommand(tool *mcp.Tool, name string, before func(cmd *cobra.Command, args map[string]any) error) *cobra.Command {
	schema, err := ParseSchema(tool.InputSchema)
	if err != nil {
		fmt.Fprintf(a.stderr, "thetix: skipping tool %s: %v\n", tool.Name, err)
		return nil
	}
	usage := name
	for _, required := range schema.Required {
		usage += " <" + FlagName(required) + ">"
	}
	short, long := describe(tool)
	cmd := &cobra.Command{
		Use:     usage,
		Short:   short,
		Long:    long,
		GroupID: toolGroup(tool),
		Args:    cobra.MaximumNArgs(len(schema.Required)),
	}
	values := map[string]*string{}
	booleans := map[string]*bool{}
	for _, property := range schema.Names() {
		p := schema.Properties[property]
		flag := argFlag(property)
		help := p.Description
		if len(p.Enum) > 0 {
			help += " (one of: " + enumText(p.Enum) + ")"
		}
		value := &typedValue{}
		switch {
		case p.Type == "boolean":
			def, _ := p.Default.(bool)
			booleans[property] = cmd.Flags().Bool(flag, def, help)
			continue
		case p.ContentEncoding == "base64":
			value.kind, help = "file", help+" Give a file path, or - for standard input."
		case IsListFlag(p):
			value.kind, help = "list", help+" Comma-separated (or a JSON array)."
		case p.Type == "array" || p.Type == "object":
			value.kind, help = "json", help+" JSON, or @file.json (- for standard input)."
		case p.Type == "integer":
			value.kind = "int"
		case p.Type == "number":
			value.kind = "number"
		default:
			value.kind = "string"
		}
		cmd.Flags().Var(value, flag, help)
		values[property] = &value.text
	}
	rawArgs := cmd.Flags().String("args", "", "all arguments as one JSON object, @file.json or - for stdin (flags override it)")

	cmd.RunE = func(cmd *cobra.Command, positional []string) error {
		args, err := parseArgsObject(*rawArgs, a.stdin)
		if err != nil {
			return err
		}
		files := map[string]string{}
		set := func(property, value string) error {
			converted, err := schema.Convert(property, value, a.stdin)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			args[property] = converted
			if schema.Properties[property].ContentEncoding == "base64" {
				files[property] = value
			}
			return nil
		}
		for i, value := range positional {
			if err := set(schema.Required[i], value); err != nil {
				return err
			}
		}
		for property, value := range values {
			if cmd.Flags().Changed(argFlag(property)) {
				if err := set(property, *value); err != nil {
					return err
				}
			}
		}
		for property, value := range booleans {
			if cmd.Flags().Changed(argFlag(property)) {
				args[property] = *value
			}
		}
		schema.FillFileFields(args, files)
		if before != nil {
			if err := before(cmd, args); err != nil {
				return err
			}
		}
		if missing := schema.Missing(args); len(missing) > 0 {
			flags := make([]string, len(missing))
			for i, m := range missing {
				flags[i] = "--" + argFlag(m)
			}
			return fail(ExitUsage, "%s needs %s (see `thetix %s --help`)", name, strings.Join(flags, ", "), name)
		}
		if isDestructive(tool) && !a.yes {
			if err := a.confirm(tool); err != nil {
				return err
			}
		}
		return a.run(cmd.Context(), tool.Name, args)
	}
	return cmd
}

// confirm asks before a destructive tool runs; without a terminal it refuses.
func (a *app) confirm(tool *mcp.Tool) error {
	if !isTerminal(a.stdin) || !isTerminal(a.stderr) {
		return fail(ExitRefused, "%s is destructive; pass --yes to run it", CommandName(tool.Name))
	}
	fmt.Fprintf(a.stderr, "%s: %s\nContinue? [y/N] ", tool.Title, strings.SplitN(tool.Description, "\n", 2)[0])
	answer, _ := bufio.NewReader(a.stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
		return fail(ExitRefused, "cancelled")
	}
	return nil
}

// run calls a tool and prints its result. When the result waits for the
// person in Thetix (an approval, the free trial's checkout, a brokerage
// sign-in), it shows the link, opens it, and calls again with the id, which
// waits for the person; see waits for when it does.
func (a *app) run(ctx context.Context, tool string, args map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	session, creds, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	showProgress := !a.noProgress && !a.asJSON && isTerminal(a.stderr)
	shown := &shownLinks{seen: map[string]bool{}}
	// Progress is always followed: a link the person must open reaches them
	// even when ordinary progress is not printed.
	progress := func(message string, _ float64) {
		if link := personLink(message); link != "" {
			a.showLink(link, creds.Server, shown)
			return
		}
		if showProgress && message != "" {
			fmt.Fprintf(a.stderr, "· %s\n", message)
		}
	}
	var result *mcp.CallToolResult
	for {
		result, err = session.Call(ctx, tool, args, progress)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if idArg, id := waitingFor(args); id != "" {
				return fail(ExitApproval, "still waiting for you in Thetix after --timeout %s; run the same command "+
					"again with --%s %s to keep waiting", a.timeout, argFlag(idArg), id)
			}
			return fail(ExitNetwork, "%s did not finish within --timeout %s", tool, a.timeout)
		}
		if err != nil {
			return connectionError(creds.Server, err)
		}
		idArg, id, link := personWait(result)
		if id == "" {
			break
		}
		a.showLink(link, creds.Server, shown)
		if !a.waits(args[idArg] != nil) {
			printResult(a.stdout, a.stderr, result, a.asJSON)
			return fail(ExitApproval, "waiting for you in Thetix (the link above); then run the same command again "+
				"with --%s %s, which waits for it (with `thetix call`: %s=%s)", argFlag(idArg), id, idArg, id)
		}
		if args[idArg] == nil && !a.asJSON {
			fmt.Fprintln(a.stderr, "  Waiting for you to finish in Thetix (Ctrl-C to stop; the link stays valid)…")
		}
		args[idArg] = id
	}
	if tool == askTool {
		a.rememberChat(creds.Server, result)
	}
	code := printResult(a.stdout, a.stderr, result, a.asJSON)
	if scope, ok := missingScope(result); ok {
		// The cached tool list may predate a permission change.
		forgetTools(creds)
		if scope == "" {
			scope = "<permission>"
		}
		return fail(ExitScope, "this connection lacks the %s permission: grant it in Thetix under Profile > API Keys, "+
			"or run `thetix login --allow %s`", scope, scope)
	}
	if action := planAction(result); action != "" {
		return fail(ExitPlan, "%s", planAdvice(action))
	}
	if code != ExitOK {
		return &exitError{code: code, err: errors.New("")}
	}
	return nil
}

// personWait is what a result waits for the person to do in Thetix: the
// argument to call again with (approval_id or step_id), its id and the link.
// The id is "" when the result waits for nothing.
func personWait(result *mcp.CallToolResult) (idArg, id, link string) {
	if id, link, ok := pendingApproval(result); ok {
		return "approval_id", id, link
	}
	if id, link, ok := pendingStep(result); ok {
		return "step_id", id, link
	}
	return "", "", ""
}

// waitingFor is the id a call already waits on, if any.
func waitingFor(args map[string]any) (idArg, id string) {
	for _, arg := range []string{"approval_id", "step_id"} {
		if v, _ := args[arg].(string); v != "" {
			return arg, v
		}
	}
	return "", ""
}

// waits says whether to wait for the person: always when the command was run
// with the id (that is what it asks for), never with --no-wait, and otherwise
// only for a person at a terminal. A script or an AI agent gets the link and
// the id at once (exit 6), so it can hand the link on and run again with the id.
func (a *app) waits(hasID bool) bool {
	if a.noWait {
		return false
	}
	return hasID || (isTerminal(a.stdin) && isTerminal(a.stderr))
}

// planAdvice is what to tell the person when their plan doesn't include something.
func planAdvice(action string) string {
	switch action {
	case "start_trial":
		return "your Thetix plan doesn't include this yet: run `thetix start-free-trial` to start the free trial"
	case "reactivate":
		return "your Thetix trial or plan has ended: run `thetix start-free-trial` to reactivate it"
	case "wait_for_reset":
		return "you've used today's allowance on your Thetix plan; it resets tomorrow"
	default:
		return "your Thetix plan doesn't include this; see `thetix list-plans`, or upgrade in Thetix under Profile > Subscription"
	}
}

// shownLinks are the links already shown for one command. Progress arrives
// on the session's goroutine, so it is locked.
type shownLinks struct {
	mu   sync.Mutex
	seen map[string]bool
}

// showLink prints a link the person must open, once, prominently, and opens
// it in the browser unless --no-browser (or THETIX_NO_BROWSER) says not to.
// Only a Thetix page on the server the CLI talks to is ever opened.
func (a *app) showLink(link, server string, shown *shownLinks) {
	shown.mu.Lock()
	defer shown.mu.Unlock()
	if link == "" || shown.seen[link] {
		return
	}
	shown.seen[link] = true
	if !linkOnServer(link, server) {
		fmt.Fprintf(a.stderr, "\n  The server sent a link that is not a Thetix page on %s; not opening it:\n    %s\n\n", server, link)
		return
	}
	heading := map[string]string{
		"approve": "Approve this in Thetix (signed in, within 15 minutes):",
		"trial":   "Start your free trial in Thetix (signed in; your card goes to Thetix's checkout):",
		"broker":  "Connect your brokerage in Thetix (signed in; you sign in to your broker there):",
	}[linkKind(link)]
	fmt.Fprintf(a.stderr, "\n  %s\n\n    %s\n\n", heading, link)
	if !a.noBrowser {
		if err := auth.OpenBrowser(link); err != nil {
			fmt.Fprintf(a.stderr, "  (Could not open a browser: %v)\n", err)
		}
	}
}

// rememberChat keeps the last chat id so `thetix ask --continue` can follow up.
func (a *app) rememberChat(server string, result *mcp.CallToolResult) {
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return
	}
	chatID, _ := structured["chat_id"].(string)
	if chatID == "" {
		return
	}
	account := a.cfg.Accounts[server]
	if account == nil {
		account = &config.Account{}
		a.cfg.Accounts[server] = account
	}
	account.LastChatID = chatID
	if err := a.cfg.Save(); err != nil {
		fmt.Fprintf(a.stderr, "thetix: could not remember the chat: %v\n", err)
	}
}

// askCommand is `thetix ask "question"`: ask_thetix with --continue.
func (a *app) askCommand(tool *mcp.Tool) *cobra.Command {
	var follow bool
	var cmd *cobra.Command
	cmd = a.toolCommand(tool, "ask", func(_ *cobra.Command, args map[string]any) error {
		if !follow {
			return nil
		}
		if _, ok := args["chat_id"]; ok {
			return fail(ExitUsage, "use --continue or --chat-id, not both")
		}
		creds, err := a.credentials()
		if err != nil {
			return err
		}
		account := a.cfg.Accounts[creds.Server]
		if account == nil || account.LastChatID == "" {
			return fail(ExitUsage, "no earlier chat to continue; ask without --continue first")
		}
		args["chat_id"] = account.LastChatID
		return nil
	})
	if cmd == nil {
		return nil
	}
	cmd.Short = "Ask Thetix a question (ask-thetix; --continue follows up on the last answer)"
	cmd.Flags().BoolVar(&follow, "continue", false, "continue the last chat started with `thetix ask`")
	return cmd
}

func (a *app) toolsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List the tools the Thetix server offers (and refresh the command list)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			session, creds, err := a.connect(ctx)
			if err != nil {
				return err
			}
			defer session.Close()
			tools, err := session.Tools(ctx)
			if err != nil {
				return connectionError(creds.Server, err)
			}
			saveTools(creds, tools)
			if a.asJSON {
				encoder := json.NewEncoder(a.stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(tools)
			}
			sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
			titles := []struct{ ID, Title string }{}
			titles = append(titles, toolGroups...)
			titles = append(titles, struct{ ID, Title string }{groupTools, "Other tools:"})
			first := true
			for _, group := range titles {
				var members []*mcp.Tool
				for _, tool := range tools {
					if toolGroup(tool) == group.ID {
						members = append(members, tool)
					}
				}
				if len(members) == 0 {
					continue
				}
				if !first {
					fmt.Fprintln(a.stdout)
				}
				first = false
				fmt.Fprintln(a.stdout, group.Title)
				for _, tool := range members {
					var marks []string
					if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
						marks = append(marks, "read-only")
					} else if isDestructive(tool) {
						marks = append(marks, "destructive")
					}
					if scope := toolScope(tool); scope == "trade" || scope == "admin" {
						marks = append(marks, scope)
					}
					marker := ""
					if len(marks) > 0 {
						marker = " (" + strings.Join(marks, ", ") + ")"
					}
					short, _ := describe(tool)
					fmt.Fprintf(a.stdout, "  %-30s %s%s\n", CommandName(tool.Name), short, marker)
				}
			}
			fmt.Fprintf(a.stdout, "\n%d tools. Run `thetix <tool> --help` for a tool's arguments.\n", len(tools))
			if permissions := permissionsText("", tools); permissions != "" {
				fmt.Fprintf(a.stdout, "This connection's permissions: %s.\n", permissions)
			}
			return nil
		},
	}
}

func (a *app) callCommand() *cobra.Command {
	var rawArgs string
	cmd := &cobra.Command{
		Use:   "call <tool> [name=value ...]",
		Short: "Call any tool by its MCP name with name=value arguments",
		Long: "Call a tool by its MCP name (e.g. list_ideas) with name=value arguments typed by the tool's schema,\n" +
			"or all arguments as JSON with --args. Useful in scripts that should not depend on command names.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, positional []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			session, creds, err := a.connect(ctx)
			if err != nil {
				cancel()
				return err
			}
			tools, err := session.Tools(ctx)
			var tool *mcp.Tool
			for _, t := range tools {
				if t.Name == positional[0] {
					tool = t
				}
			}
			scope, lacksScope := "", false
			if err == nil && tool == nil {
				scope, lacksScope = unlistedToolScope(ctx, session, positional[0])
			}
			session.Close()
			cancel()
			if err != nil {
				return connectionError(creds.Server, err)
			}
			if lacksScope {
				return fail(ExitScope, "%s needs the %s permission, which this connection lacks: grant it in Thetix "+
					"under Profile > API Keys, or run `thetix login --allow %s`", positional[0], scope, scope)
			}
			if tool == nil {
				return fail(ExitUsage, "%s has no tool %q; run `thetix tools`", creds.Server, positional[0])
			}
			schema, err := ParseSchema(tool.InputSchema)
			if err != nil {
				return fail(ExitNetwork, "tool %s: %v", tool.Name, err)
			}
			args, err := parseArgsObject(rawArgs, a.stdin)
			if err != nil {
				return err
			}
			files := map[string]string{}
			for _, pair := range positional[1:] {
				name, value, ok := strings.Cut(pair, "=")
				if !ok {
					return fail(ExitUsage, "%q is not name=value", pair)
				}
				if _, declared := schema.Properties[name]; !declared {
					return fail(ExitUsage, "%s has no argument %q (it takes: %s)", tool.Name, name, strings.Join(schema.Names(), ", "))
				}
				converted, err := schema.Convert(name, value, a.stdin)
				if err != nil {
					return fail(ExitUsage, "%v", err)
				}
				args[name] = converted
				if schema.Properties[name].ContentEncoding == "base64" {
					files[name] = value
				}
			}
			schema.FillFileFields(args, files)
			if isDestructive(tool) && !a.yes {
				if err := a.confirm(tool); err != nil {
					return err
				}
			}
			return a.run(cmd.Context(), tool.Name, args)
		},
	}
	cmd.Flags().StringVar(&rawArgs, "args", "", "all arguments as one JSON object, @file.json or - for stdin")
	return cmd
}

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the thetix version",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintf(a.stdout, "thetix %s\n", a.version)
		},
	}
}

// probeArgument is an argument no tool takes.
const probeArgument = "thetix_cli_probe"

// unlistedToolScope asks the server why it doesn't list a tool: the server
// lists only the tools this connection's permissions allow, and answers a
// call of one it withholds with the permission it needs. The server checks
// the permission before the arguments and refuses unknown arguments, so
// with probeArgument the tool never runs.
func unlistedToolScope(ctx context.Context, session *remote.Session, name string) (string, bool) {
	result, err := session.Call(ctx, name, map[string]any{probeArgument: true}, nil)
	if err != nil {
		return "", false
	}
	return missingScope(result)
}
