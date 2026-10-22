package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thetaedge/thetix-cli/internal/skills"
)

// skillsCommand installs the thetix Agent Skill for coding agents (Claude
// Code, Codex, Cursor, Gemini CLI, OpenClaw, OpenCode, GitHub Copilot). It
// works offline and needs no login.
func (a *app) skillsCommand() *cobra.Command {
	var agentIDs []string
	var project bool
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Teach your coding agents to use thetix (install the Thetix skill)",
		Long: "Install the Thetix skill (a SKILL.md) where coding agents look for skills, so Claude Code, Codex,\n" +
			"Cursor, Gemini CLI, OpenClaw, OpenCode or GitHub Copilot use thetix for finance and trading tasks.\n" +
			"Run `thetix skills install` again after upgrading thetix to update the skill.",
	}
	addFlags := func(c *cobra.Command) {
		c.Flags().StringSliceVar(&agentIDs, "agents", nil,
			"agents to install for, comma-separated ("+skills.IDs()+"); default: claude and agents, plus the others that are installed")
		c.Flags().BoolVar(&project, "project", false, "use the current directory's skills folders (.claude/skills, ...) instead of your home directory")
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "Install or update the Thetix skill for your coding agents",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			base, chosen, err := a.skillTargets(agentIDs, project)
			if err != nil {
				return err
			}
			skill := skills.Render(a.version)
			var rows [][]string
			var legacy []string
			failed := false
			for _, agent := range chosen {
				dir := agent.Dir(base, project)
				outcome, err := skills.Install(dir, skill)
				status := string(outcome)
				if err != nil {
					failed = true
					status = "not installed: " + err.Error()
				}
				rows = append(rows, []string{agent.Name, status, dir})
				if old := agent.LegacyDir(base, project); old != "" {
					legacy = append(legacy, old)
				}
			}
			a.printSkillRows([]string{"agent", "status", "path"}, rows)
			for _, old := range legacy {
				fmt.Fprintf(a.stderr, "\nThe older Thetix skill (the thetix-skill repository) is still at %s; your agent "+
					"would load both. Remove it with:\n  rm -rf %q\n", old, old)
			}
			if failed {
				return fail(ExitToolError, "the skill was not installed everywhere (see above)")
			}
			if !a.asJSON {
				fmt.Fprintln(a.stdout, "\nAgents load the skill in a new session. Sign in once with `thetix login` if you haven't.")
			}
			return nil
		},
	}
	addFlags(install)

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Thetix skill from your coding agents",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			base, chosen, err := a.skillTargets(agentIDs, project)
			if err != nil {
				return err
			}
			if len(agentIDs) == 0 {
				chosen = skills.Agents // remove it wherever it is
			}
			var rows [][]string
			for _, agent := range chosen {
				dir := agent.Dir(base, project)
				removed, err := skills.Uninstall(dir)
				status := "not installed"
				switch {
				case err != nil:
					status = "left: " + err.Error()
				case removed:
					status = "removed"
				}
				rows = append(rows, []string{agent.Name, status, dir})
			}
			a.printSkillRows([]string{"agent", "status", "path"}, rows)
			return nil
		},
	}
	addFlags(uninstall)

	list := &cobra.Command{
		Use:   "list",
		Short: "Show the supported agents and where the skill is installed",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			base, err := skillBase(project)
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()
			var rows [][]string
			for _, agent := range skills.Agents {
				dir := agent.Dir(base, project)
				state := "not installed"
				if version, ours := skills.Installed(dir); ours {
					state = "installed (" + version + ")"
					if version != a.version {
						state += ", update with `thetix skills install`"
					}
				}
				detected := "no"
				if agent.Always {
					detected = "always"
				} else if home != "" && agent.Detected(home) {
					detected = "yes"
				}
				rows = append(rows, []string{agent.ID, agent.Reads, detected, state, dir})
			}
			a.printSkillRows([]string{"id", "read by", "default", "skill", "path"}, rows)
			return nil
		},
	}
	list.Flags().BoolVar(&project, "project", false, "show the current directory's skills folders")

	printCmd := &cobra.Command{
		Use:   "print",
		Short: "Print the Thetix skill (SKILL.md) this version installs",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprint(a.stdout, skills.Render(a.version))
		},
	}

	cmd.AddCommand(install, uninstall, list, printCmd)
	return cmd
}

// skillBase is the directory the agents' skills folders are under.
func skillBase(project bool) (string, error) {
	if project {
		dir, err := os.Getwd()
		if err != nil {
			return "", fail(ExitUsage, "cannot read the current directory: %v", err)
		}
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fail(ExitUsage, "cannot find your home directory (set HOME), or use --project")
	}
	return home, nil
}

// skillTargets resolves --agents (default: skills.Defaults) and the base directory.
func (a *app) skillTargets(ids []string, project bool) (string, []skills.Agent, error) {
	base, err := skillBase(project)
	if err != nil {
		return "", nil, err
	}
	if len(ids) == 0 {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = base
		}
		return base, skills.Defaults(home), nil
	}
	var chosen []skills.Agent
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "all" {
			return base, skills.Agents, nil
		}
		agent, ok := skills.Lookup(id)
		if !ok {
			return "", nil, fail(ExitUsage, "unknown agent %q; choose from %s, or all", id, skills.IDs())
		}
		if !seen[id] {
			seen[id] = true
			chosen = append(chosen, agent)
		}
	}
	return base, chosen, nil
}

// printSkillRows prints a table, or with --json a list of objects.
func (a *app) printSkillRows(header []string, rows [][]string) {
	if a.asJSON {
		out := make([]map[string]string, len(rows))
		for i, row := range rows {
			out[i] = map[string]string{}
			for j, key := range header {
				out[i][strings.ReplaceAll(key, " ", "_")] = row[j]
			}
		}
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(out); err != nil {
			fmt.Fprintln(a.stderr, err)
		}
		return
	}
	w := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, strings.ToUpper(strings.Join(header, "\t")))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	if err := w.Flush(); err != nil && !errors.Is(err, os.ErrClosed) {
		fmt.Fprintln(a.stderr, err)
	}
}
