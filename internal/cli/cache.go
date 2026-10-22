package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thetaedge/thetix-cli/internal/config"
)

// The tool list is cached per server and credential so `thetix --help` and
// tool commands work without a round trip; `thetix tools` refreshes it. The
// server lists only the tools a connection's permissions allow, so another
// key (or the same key after its permissions change) needs its own list: a
// new key gets a new cache file, and a permission error forgets the file.
const cacheTTL = 24 * time.Hour

type toolCache struct {
	Server    string      `json:"server"`
	FetchedAt time.Time   `json:"fetched_at"`
	Tools     []*mcp.Tool `json:"tools"`
}

func cachePath(creds config.Credentials) string {
	dir := os.Getenv("THETIX_CACHE_DIR")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(base, "thetix")
	}
	sum := sha256.Sum256([]byte(creds.Server + "\n" + creds.Token))
	return filepath.Join(dir, "tools-"+hex.EncodeToString(sum[:8])+".json")
}

// loadTools returns the cached tools for a server and credential and whether they are fresh.
func loadTools(creds config.Credentials) ([]*mcp.Tool, bool) {
	path := cachePath(creds)
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cache toolCache
	if json.Unmarshal(data, &cache) != nil || cache.Server != creds.Server {
		return nil, false
	}
	return cache.Tools, time.Since(cache.FetchedAt) < cacheTTL
}

func saveTools(creds config.Credentials, tools []*mcp.Tool) {
	path := cachePath(creds)
	if path == "" {
		return
	}
	data, err := json.Marshal(toolCache{Server: creds.Server, FetchedAt: time.Now(), Tools: tools})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

func forgetTools(creds config.Credentials) {
	if path := cachePath(creds); path != "" {
		_ = os.Remove(path)
	}
}
