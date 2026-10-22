// Package config stores the servers thetix has logged in to and resolves
// which server and credential a command uses.
//
// The file is JSON at <user config dir>/thetix/config.json (~/.config/thetix
// on Linux, ~/Library/Application Support/thetix on macOS, %AppData%\thetix
// on Windows), written with mode 0600 because it holds API keys.
// THETIX_CONFIG_DIR overrides the directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/thetaedge/thetix-cli/internal/contract"
)

// DefaultServer is Thetix's MCP endpoint.
const DefaultServer = "https://api.thetix.ai/mcp"

// Auth methods an Account records.
const (
	AuthOAuth  = "oauth"   // issued by `thetix login` in the browser; logout revokes it
	AuthAPIKey = "api_key" // pasted by the person; logout only forgets it
)

// Account is one server's stored login.
type Account struct {
	Token string `json:"token"`
	Auth  string `json:"auth"`
	// Scope is the permissions the person granted at a browser login
	// (space-separated, e.g. "read act trade"); empty for a pasted key. They
	// can change later in Profile > Agent Access; `thetix status` shows the live ones.
	Scope      string `json:"scope,omitempty"`
	LastChatID string `json:"last_chat_id,omitempty"`
}

// Config is the whole config file. Invariant: every Accounts key is a
// normalized server URL.
type Config struct {
	Default  string              `json:"default_server,omitempty"`
	Accounts map[string]*Account `json:"accounts,omitempty"`

	path string
}

// Dir is the directory thetix keeps its config in.
func Dir() (string, error) {
	if dir := os.Getenv("THETIX_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no config directory: %w", err)
	}
	return filepath.Join(base, "thetix"), nil
}

// Load reads the config file; a missing file is an empty config.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Accounts: map[string]*Account{}, path: filepath.Join(dir, "config.json")}
	data, err := os.ReadFile(cfg.path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v); fix or delete it", cfg.path, err)
	}
	if cfg.Accounts == nil {
		cfg.Accounts = map[string]*Account{}
	}
	return cfg, nil
}

// Path is the config file's location.
func (c *Config) Path() string { return c.path }

// Save writes the config atomically with mode 0600.
func (c *Config) Save() error {
	contract.Require(c.path != "", "Save needs a config from Load")
	for server := range c.Accounts {
		normalized, err := NormalizeServer(server)
		contract.Invariant(err == nil && normalized == server, "account key %q is a normalized server URL", server)
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// NormalizeServer turns what a person types into the MCP endpoint URL:
// "api.thetix.ai" and "https://api.thetix.ai" both become
// "https://api.thetix.ai/mcp". http is allowed only for a local server.
func NormalizeServer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty server URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname())) {
		return "", fmt.Errorf("%q must use https (http only for localhost)", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/mcp"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Credentials say which server a command talks to and with what token.
type Credentials struct {
	Server string
	Token  string // empty when not logged in
	Source string // where Token came from, for `thetix status`
}

// Resolve picks the server (flag, THETIX_SERVER, the config's default,
// THETAEDGE_API_BASE, then Thetix) and its token (flag, THETIX_API_KEY, the
// stored login for that server, then THETAEDGE_API_KEY). The THETAEDGE_*
// variables are the thetix skill's; an explicit `thetix login` outranks them.
func (c *Config) Resolve(flagServer, flagToken string) (Credentials, error) {
	server := DefaultServer
	for _, candidate := range []string{flagServer, os.Getenv("THETIX_SERVER"), c.Default, os.Getenv("THETAEDGE_API_BASE")} {
		if candidate != "" {
			server = candidate
			break
		}
	}
	server, err := NormalizeServer(server)
	if err != nil {
		return Credentials{}, err
	}
	creds := Credentials{Server: server}
	switch {
	case flagToken != "":
		creds.Token, creds.Source = flagToken, "--api-key flag"
	case os.Getenv("THETIX_API_KEY") != "":
		creds.Token, creds.Source = os.Getenv("THETIX_API_KEY"), "THETIX_API_KEY"
	case c.Accounts[server] != nil && c.Accounts[server].Token != "":
		creds.Token, creds.Source = c.Accounts[server].Token, fmt.Sprintf("%s login in %s", c.Accounts[server].Auth, c.path)
	case os.Getenv("THETAEDGE_API_KEY") != "":
		creds.Token, creds.Source = os.Getenv("THETAEDGE_API_KEY"), "THETAEDGE_API_KEY"
	}
	contract.Ensure(creds.Server != "", "a resolved server")
	return creds, nil
}
