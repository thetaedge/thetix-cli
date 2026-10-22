// Package skills installs the thetix Agent Skill (SKILL.md, agentskills.io)
// into the skills directories coding agents read, so an agent knows to use
// the thetix command line for finance and trading tasks.
//
// The skill is embedded in the binary and stamped with its version, so each
// release installs the skill that matches its own commands.
package skills

import (
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thetaedge/thetix-cli/internal/contract"
)

//go:embed SKILL.md
var template string

// Name is the skill's directory and its frontmatter name.
const Name = "thetix"

const versionMarker = "__THETIX_VERSION__"

var versionLine = regexp.MustCompile(`(?m)^\s*thetix_cli_version:\s*"([^"]*)"`)

// Render is the SKILL.md this binary installs.
func Render(version string) string {
	contract.Require(strings.Contains(template, versionMarker), "the embedded SKILL.md carries the version marker")
	if version == "" {
		version = "dev"
	}
	// LF everywhere, even if a checkout gave the embedded file CRLF.
	out := strings.ReplaceAll(strings.ReplaceAll(template, "\r\n", "\n"), versionMarker, version)
	contract.Ensure(strings.HasPrefix(out, "---\nname: "+Name+"\n"), "the skill starts with its frontmatter name")
	return out
}

// Agent is a coding agent and where it reads skills.
type Agent struct {
	ID    string
	Name  string
	Reads string // which agents read this directory, for `thetix skills list`
	// User and Project are the skills directories, relative to the home
	// directory and to the project directory.
	User, Project string
	// Always is in the default install set; otherwise only when detected.
	Always bool
	// Detect: a directory under home, or a program on PATH, that shows the
	// agent is installed.
	DetectDirs, DetectBins []string
}

// Agents are the supported agents in display order. ~/.agents/skills is the
// shared location Codex, Cursor, Gemini CLI and OpenClaw read.
var Agents = []Agent{
	{ID: "claude", Name: "Claude Code", Reads: "Claude Code",
		User: ".claude/skills", Project: ".claude/skills", Always: true,
		DetectDirs: []string{".claude"}, DetectBins: []string{"claude"}},
	{ID: "agents", Name: "Shared agents directory", Reads: "Codex, Cursor, Gemini CLI, OpenClaw",
		User: ".agents/skills", Project: ".agents/skills", Always: true,
		DetectDirs: []string{".agents", ".codex", ".cursor", ".gemini", ".openclaw"},
		DetectBins: []string{"codex", "cursor-agent", "gemini", "openclaw"}},
	{ID: "opencode", Name: "OpenCode", Reads: "OpenCode",
		User: ".config/opencode/skills", Project: ".opencode/skills",
		DetectDirs: []string{".config/opencode"}, DetectBins: []string{"opencode"}},
	{ID: "copilot", Name: "GitHub Copilot", Reads: "GitHub Copilot",
		User: ".copilot/skills", Project: ".github/skills",
		DetectDirs: []string{".copilot"}, DetectBins: []string{"copilot"}},
}

// Lookup is the agent with this id.
func Lookup(id string) (Agent, bool) {
	for _, a := range Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// IDs are the supported agent ids, comma-separated.
func IDs() string {
	ids := make([]string, len(Agents))
	for i, a := range Agents {
		ids[i] = a.ID
	}
	return strings.Join(ids, ", ")
}

// Detected says whether the agent looks installed for the person whose home
// directory is home.
func (a Agent) Detected(home string) bool {
	for _, d := range a.DetectDirs {
		if info, err := os.Stat(filepath.Join(home, d)); err == nil && info.IsDir() {
			return true
		}
	}
	for _, b := range a.DetectBins {
		if _, err := exec.LookPath(b); err == nil {
			return true
		}
	}
	return false
}

// Defaults are the agents `thetix skills install` installs for when none
// are named: the always-installed ones, and the others that are detected.
func Defaults(home string) []Agent {
	var out []Agent
	for _, a := range Agents {
		if a.Always || a.Detected(home) {
			out = append(out, a)
		}
	}
	contract.Ensure(len(out) > 0, "the default set is never empty")
	return out
}

// Dir is the agent's skill directory for thetix: under base, which is the
// home directory, or the project directory when project is set.
func (a Agent) Dir(base string, project bool) string {
	contract.Require(base != "", "a base directory")
	rel := a.User
	if project {
		rel = a.Project
	}
	return filepath.Join(base, filepath.FromSlash(rel), Name)
}

// Installed is the version of the thetix skill in dir, and whether the
// SKILL.md there is one this CLI wrote (named thetix, with its
// thetix_cli_version). Any other SKILL.md is not ours.
func Installed(dir string) (version string, ours bool) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(data), "---\nname: "+Name+"\n") {
		return "", false
	}
	m := versionLine.FindStringSubmatch(string(data))
	if m == nil || m[1] == "" {
		return "", false
	}
	return m[1], true
}

// LegacyDir is where the retired thetix-skill repository was cloned under an
// agent's skills directory (its SKILL.md is also named thetix, so an agent
// would load both), or "" when there is none.
func (a Agent) LegacyDir(base string, project bool) string {
	dir := filepath.Join(filepath.Dir(a.Dir(base, project)), "thetix-skill")
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return ""
	}
	return dir
}

// Outcome of installing into one directory.
type Outcome string

const (
	Created   Outcome = "installed"
	Updated   Outcome = "updated"
	Unchanged Outcome = "up to date"
)

// ErrNotOurs: the directory holds a different skill named thetix, which
// install and uninstall leave alone.
var ErrNotOurs = errors.New("a different skill named thetix is there; left as it is")

// Install writes skill as dir/SKILL.md, atomically (a temp file renamed into
// place).
func Install(dir, skill string) (Outcome, error) {
	contract.Require(strings.HasPrefix(skill, "---\nname: "+Name+"\n"), "a rendered thetix skill")
	path := filepath.Join(dir, "SKILL.md")
	existing, err := os.ReadFile(path)
	outcome := Created
	switch {
	case err == nil && string(existing) == skill:
		return Unchanged, nil
	case err == nil:
		if _, ours := Installed(dir); !ours {
			return "", ErrNotOurs
		}
		outcome = Updated
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".SKILL.md.*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(skill); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	v, ours := Installed(dir)
	contract.Ensure(ours && v != "", "the skill just written at %s reads back as ours", dir)
	return outcome, nil
}

// Uninstall removes the thetix skill directory dir. Reports whether there
// was one to remove.
func Uninstall(dir string) (bool, error) {
	contract.Require(filepath.Base(dir) == Name, "uninstall only removes a %q skill directory", Name)
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if _, ours := Installed(dir); !ours {
		return false, ErrNotOurs
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}
