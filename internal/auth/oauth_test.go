package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAuthServer is an MCP resource plus OAuth server that checks the flow
// the way Thetix does: registered redirect, S256 PKCE, single-use code.
func fakeAuthServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var challenge, redirect, scope string
	issued := map[string]bool{}
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize",
			"token_endpoint": base + "/token", "registration_endpoint": base + "/register", "revocation_endpoint": base + "/revoke",
			"code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var reg struct {
			RedirectURIs []string `json:"redirect_uris"`
			Method       string   `json:"token_endpoint_auth_method"`
		}
		json.NewDecoder(r.Body).Decode(&reg)
		if reg.Method != "none" || len(reg.RedirectURIs) != 1 {
			http.Error(w, "bad registration", 400)
			return
		}
		redirect = reg.RedirectURIs[0]
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": "client-1"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "client-1" || q.Get("redirect_uri") != redirect || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad authorization request", 400)
			return
		}
		challenge = q.Get("code_challenge")
		scope = q.Get("scope")
		issued["code-1"] = true
		http.Redirect(w, r, redirect+"?code=code-1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if !issued[r.Form.Get("code")] || Challenge(r.Form.Get("code_verifier")) != challenge || r.Form.Get("redirect_uri") != redirect {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		delete(issued, r.Form.Get("code"))
		json.NewEncoder(w).Encode(map[string]any{"access_token": "te_issued", "token_type": "Bearer",
			"scope": strings.TrimSpace(strings.ReplaceAll(scope, "admin", ""))})
	})
	ts := httptest.NewServer(mux)
	base = ts.URL
	t.Cleanup(ts.Close)
	return ts, &scope
}

func TestScopeParam(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{{nil, "read"}, {DefaultScopes, "read act"}, {[]string{"admin", "act", "trade"}, "read act trade admin"},
		{[]string{"trade", "trade"}, "read trade"}} {
		if got := ScopeParam(c.in); got != c.want {
			t.Errorf("ScopeParam(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if IsPermission("thetix") || !IsPermission("admin") {
		t.Error("IsPermission")
	}
}

func TestLoginRunsTheWholeFlow(t *testing.T) {
	ts, askedScope := fakeAuthServer(t)
	out := &syncBuffer{}
	done := make(chan struct{})
	var token Token
	var err error
	go func() {
		defer close(done)
		token, err = Login(context.Background(), ts.Client(), LoginOptions{Server: ts.URL + "/mcp", ClientName: "test",
			Out: out, Timeout: 10 * time.Second, Scopes: []string{"trade"}})
	}()
	// Play the browser: open the printed URL and follow redirects to the loopback.
	authURL := regexp.MustCompile(`http://\S+/authorize\?\S+`)
	var link string
	for i := 0; i < 100 && link == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		link = authURL.FindString(out.String())
	}
	if link == "" {
		t.Fatalf("no authorization URL printed: %q", out.String())
	}
	resp, getErr := http.Get(link)
	if getErr != nil {
		t.Fatal(getErr)
	}
	resp.Body.Close()
	<-done
	if err != nil || token.AccessToken != "te_issued" {
		t.Fatalf("Login = %+v, %v", token, err)
	}
	// The fake grants what was asked, minus admin (read is always asked for).
	if *askedScope != "read trade" || token.Scope != "read trade" {
		t.Fatalf("asked for scope %q, granted %q; want read trade", *askedScope, token.Scope)
	}
}

func TestChallengeIsS256(t *testing.T) {
	// RFC 7636 appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("Challenge = %s", got)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }
