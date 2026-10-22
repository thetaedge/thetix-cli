package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"api.thetix.ai":                 "https://api.thetix.ai/mcp",
		"https://api.thetix.ai":         "https://api.thetix.ai/mcp",
		"https://api.thetix.ai/":        "https://api.thetix.ai/mcp",
		"https://api.thetix.ai/api/mcp": "https://api.thetix.ai/api/mcp",
		"http://localhost:3200":         "http://localhost:3200/mcp",
		"http://127.0.0.1:3290/mcp/":    "http://127.0.0.1:3290/mcp",
	}
	for in, want := range cases {
		got, err := NormalizeServer(in)
		if err != nil || got != want {
			t.Errorf("NormalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "http://example.com", "ftp://x"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Errorf("NormalizeServer(%q) accepted", bad)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Setenv("THETIX_CONFIG_DIR", t.TempDir())
	for _, v := range []string{"THETIX_SERVER", "THETIX_API_KEY", "THETAEDGE_API_KEY", "THETAEDGE_API_BASE"} {
		t.Setenv(v, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := cfg.Resolve("", "")
	if creds.Server != DefaultServer || creds.Token != "" {
		t.Fatalf("empty config resolved to %+v", creds)
	}

	t.Setenv("THETAEDGE_API_KEY", "te_legacy")
	if creds, _ = cfg.Resolve("", ""); creds.Token != "te_legacy" {
		t.Fatalf("legacy key should apply without a login, got %+v", creds)
	}
	cfg.Accounts[DefaultServer] = &Account{Token: "te_login", Auth: AuthOAuth}
	if creds, _ = cfg.Resolve("", ""); creds.Token != "te_login" {
		t.Fatalf("a login outranks THETAEDGE_API_KEY, got %+v", creds)
	}
	t.Setenv("THETIX_API_KEY", "te_env")
	if creds, _ = cfg.Resolve("", ""); creds.Token != "te_env" {
		t.Fatalf("THETIX_API_KEY outranks a login, got %+v", creds)
	}
	if creds, _ = cfg.Resolve("", "te_flag"); creds.Token != "te_flag" {
		t.Fatalf("the flag outranks everything, got %+v", creds)
	}
	if creds, _ = cfg.Resolve("http://localhost:3200", ""); creds.Server != "http://localhost:3200/mcp" {
		t.Fatalf("the --server flag picks the server, got %+v", creds)
	}
}

func TestSaveIsPrivateAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("THETIX_CONFIG_DIR", dir)
	cfg, _ := Load()
	cfg.Accounts["https://api.thetix.ai/mcp"] = &Account{Token: "te_x", Auth: AuthAPIKey, LastChatID: "c1"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("config mode %o, want 600", mode)
	}
	again, _ := Load()
	if got := again.Accounts["https://api.thetix.ai/mcp"]; got == nil || got.Token != "te_x" || got.LastChatID != "c1" {
		t.Fatalf("round trip lost the account: %+v", got)
	}
}
