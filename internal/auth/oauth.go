// Package auth logs the CLI in to a Thetix MCP server with OAuth 2.1, the
// way any MCP client connects: discover the authorization server (RFC 9728,
// RFC 8414), register (RFC 7591), send the person to the consent page in
// their browser, catch the redirect on a loopback port (RFC 8252), and trade
// the code and PKCE verifier (RFC 7636) for a token. The token is a Thetix
// API key named after this computer; the person can revoke it in
// Profile > API Keys.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/thetaedge/thetix-cli/internal/contract"
)

// Permissions a login can ask for, as the server defines them:
// read is always granted; act covers everyday changes (chats, cards,
// watchlist, settings); trade covers the cart, placing and cancelling
// orders; admin covers the workspace, accounts, brokerage connections and
// API keys. The person decides on the consent page, which starts with what
// was asked for ticked.
var Permissions = []string{"read", "act", "trade", "admin"}

// DefaultScopes are what a login asks for unless told otherwise.
var DefaultScopes = []string{"read", "act"}

// ScopeParam is the OAuth scope parameter for these permissions, in the
// server's order, always with read. Pre: every name is a Permission.
func ScopeParam(scopes []string) string {
	wanted := map[string]bool{"read": true}
	for _, scope := range scopes {
		contract.Require(IsPermission(scope), "ScopeParam of a permission, not %q", scope)
		wanted[scope] = true
	}
	var names []string
	for _, p := range Permissions {
		if wanted[p] {
			names = append(names, p)
		}
	}
	return strings.Join(names, " ")
}

// IsPermission reports whether name is one of the Permissions.
func IsPermission(name string) bool {
	for _, p := range Permissions {
		if p == name {
			return true
		}
	}
	return false
}

// Token is what the token endpoint issued: the API key, and the permissions
// the person granted (space-separated; may differ from what was asked).
type Token struct {
	AccessToken string
	Scope       string
}

// Metadata is the authorization server's RFC 8414 document, the fields used.
type Metadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	DeviceEndpoint        string   `json:"device_authorization_endpoint"`
	ChallengeMethods      []string `json:"code_challenge_methods_supported"`
}

var resourceMetadataParam = regexp.MustCompile(`resource_metadata="([^"]+)"`)

func getJSON(ctx context.Context, hc *http.Client, target string, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: not JSON metadata (%v)", target, err)
	}
	return resp.StatusCode, nil
}

// protectedResourceURL finds the RFC 9728 document: from the server's 401
// challenge, else the well-known path for the resource.
func protectedResourceURL(ctx context.Context, hc *http.Client, server string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", server, err)
	}
	resp.Body.Close()
	if m := resourceMetadataParam.FindStringSubmatch(resp.Header.Get("WWW-Authenticate")); m != nil {
		return m[1], nil
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s://%s/.well-known/oauth-protected-resource%s", u.Scheme, u.Host, u.Path), nil
}

// Discover finds the authorization server for an MCP server URL.
// Post: the metadata has authorization, token and registration endpoints and supports S256.
func Discover(ctx context.Context, hc *http.Client, server string) (*Metadata, error) {
	prmURL, err := protectedResourceURL(ctx, hc, server)
	if err != nil {
		return nil, err
	}
	var prm struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if _, err := getJSON(ctx, hc, prmURL, &prm); err != nil {
		return nil, fmt.Errorf("%s does not advertise OAuth (%v); use `thetix login --api-key` instead", server, err)
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("%s names no authorization server", prmURL)
	}
	issuer, err := url.Parse(prm.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	// RFC 8414 3: the well-known path goes between the host and the issuer's path.
	asURL := fmt.Sprintf("%s://%s/.well-known/oauth-authorization-server%s", issuer.Scheme, issuer.Host,
		strings.TrimRight(issuer.Path, "/"))
	md := &Metadata{}
	if _, err := getJSON(ctx, hc, asURL, md); err != nil {
		return nil, err
	}
	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" || md.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("%s lacks the authorization, token or registration endpoint", asURL)
	}
	s256 := false
	for _, m := range md.ChallengeMethods {
		s256 = s256 || m == "S256"
	}
	if !s256 {
		return nil, fmt.Errorf("%s does not support PKCE S256", asURL)
	}
	contract.Ensure(md.TokenEndpoint != "", "discovered a token endpoint")
	return md, nil
}

// OAuthError is an OAuth error response (RFC 6749 5.2, RFC 8628 3.5).
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Description }

func postForm(ctx context.Context, hc *http.Client, target string, form url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var oauthErr struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &oauthErr) == nil && oauthErr.Error != "" {
			return &OAuthError{Code: oauthErr.Error, Description: oauthErr.Description}
		}
		return fmt.Errorf("POST %s: HTTP %d", target, resp.StatusCode)
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(body, into)
}

// DeviceGrant is the device authorization grant type (RFC 8628).
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// Register registers a public client (no secret; PKCE or the device code
// proves it) for the given grant types (authorization_code at least).
func Register(ctx context.Context, hc *http.Client, md *Metadata, clientName, redirectURI string, grants []string) (string, error) {
	contract.Require(md != nil && md.RegistrationEndpoint != "", "Register needs discovered metadata")
	contract.Require(len(grants) > 0 && grants[0] == "authorization_code", "a client registers authorization_code first")
	payload, _ := json.Marshal(map[string]any{
		"client_name": clientName, "redirect_uris": []string{redirectURI}, "token_endpoint_auth_method": "none",
		"grant_types": grants, "response_types": []string{"code"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var reg struct {
		ClientID    string `json:"client_id"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reg); err != nil {
		return "", fmt.Errorf("registration: %v", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK || reg.ClientID == "" {
		return "", fmt.Errorf("registration refused: %s %s", reg.Error, reg.Description)
	}
	return reg.ClientID, nil
}

func randomToken() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	contract.Invariant(err == nil, "the system random source works")
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is the PKCE S256 challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// LoginOptions configure Login.
type LoginOptions struct {
	Server      string
	ClientName  string
	OpenBrowser bool
	Out         io.Writer // where the sign-in URL and status go
	Timeout     time.Duration
	Scopes      []string // permissions to ask for; nil means DefaultScopes
	// Device signs in with a code entered on any device (RFC 8628) instead of
	// a browser redirect to this machine: for SSH sessions and headless boxes.
	Device bool
}

type callback struct {
	code string
	err  error
}

// Login runs the browser sign-in and returns the issued token.
// Post: a non-empty access token on success.
func Login(ctx context.Context, hc *http.Client, opts LoginOptions) (Token, error) {
	contract.Require(opts.Server != "" && opts.Out != nil && opts.Timeout > 0, "Login needs a server, output and timeout")
	scopes := opts.Scopes
	if scopes == nil {
		scopes = DefaultScopes
	}
	scope := ScopeParam(scopes)
	run := login
	if opts.Device {
		run = deviceLogin
	}
	token, err := run(ctx, hc, opts, scope)
	if err != nil {
		return Token{}, err
	}
	if token.Scope == "" {
		// RFC 6749 5.1: a token response may omit scope when it is what was asked.
		token.Scope = scope
	}
	contract.Ensure(token.AccessToken != "", "an issued token")
	return token, nil
}

func login(ctx context.Context, hc *http.Client, opts LoginOptions, scope string) (Token, error) {
	md, err := Discover(ctx, hc, opts.Server)
	if err != nil {
		return Token{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Token{}, fmt.Errorf("cannot listen for the sign-in redirect: %w", err)
	}
	defer listener.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	clientID, err := Register(ctx, hc, md, opts.ClientName, redirectURI, []string{"authorization_code"})
	if err != nil {
		return Token{}, err
	}

	verifier, state := randomToken(), randomToken()
	query := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_challenge": {Challenge(verifier)}, "code_challenge_method": {"S256"}, "state": {state},
		"scope": {scope}, "resource": {opts.Server},
	}
	authURL := md.AuthorizationEndpoint + "?" + query.Encode()

	results := make(chan callback, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var result callback
		switch {
		case q.Get("state") != state:
			result.err = errors.New("the sign-in response did not match this login (state mismatch); run `thetix login` again")
		case q.Get("error") != "":
			result.err = fmt.Errorf("sign-in %s: %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			result.err = errors.New("the sign-in response had no code")
		default:
			result.code = q.Get("code")
		}
		message := "Thetix is connected. You can close this tab and return to the terminal."
		if result.err != nil {
			message = "Thetix was not connected: " + result.err.Error()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Thetix CLI</title>`+
			`<body style="font:16px system-ui;max-width:560px;margin:64px auto;padding:0 24px"><h1>Thetix CLI</h1><p>%s</p></body>`,
			html.EscapeString(message))
		select {
		case results <- result:
		default:
		}
	})}
	go server.Serve(listener) //nolint:errcheck // Serve returns when the listener closes.
	defer server.Close()

	fmt.Fprintf(opts.Out, "Opening your browser to connect Thetix. If it does not open, visit:\n\n  %s\n\n", authURL)
	if opts.OpenBrowser {
		if err := OpenBrowser(authURL); err != nil {
			fmt.Fprintf(opts.Out, "(Could not open a browser: %v)\n", err)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	var result callback
	select {
	case result = <-results:
	case <-waitCtx.Done():
		return Token{}, fmt.Errorf("no sign-in after %s; run `thetix login` again", opts.Timeout)
	}
	if result.err != nil {
		return Token{}, result.err
	}

	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {result.code}, "redirect_uri": {redirectURI},
		"client_id": {clientID}, "code_verifier": {verifier}, "resource": {opts.Server}}
	if err := postForm(ctx, hc, md.TokenEndpoint, form, &token); err != nil {
		return Token{}, fmt.Errorf("token exchange: %w", err)
	}
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") {
		return Token{}, errors.New("the token response had no bearer access token")
	}
	return Token{AccessToken: token.AccessToken, Scope: token.Scope}, nil
}

// deviceLogin runs the device authorization grant: the server gives a short
// code, the person enters it at the server's /connect/device page signed in
// to Thetix (on any device), and polling the token endpoint then returns the
// key. RFC 8628 3.5: authorization_pending keeps polling, slow_down adds 5
// seconds, anything else ends the login.
func deviceLogin(ctx context.Context, hc *http.Client, opts LoginOptions, scope string) (Token, error) {
	md, err := Discover(ctx, hc, opts.Server)
	if err != nil {
		return Token{}, err
	}
	if md.DeviceEndpoint == "" {
		return Token{}, errors.New("this server has no device login; run `thetix login` without --device")
	}
	// The client is registered with a loopback redirect it never listens on:
	// registration needs one, and the device grant doesn't use it.
	clientID, err := Register(ctx, hc, md, opts.ClientName, "http://127.0.0.1/callback",
		[]string{"authorization_code", DeviceGrant})
	if err != nil {
		return Token{}, err
	}
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	form := url.Values{"client_id": {clientID}, "scope": {scope}}
	if err := postForm(ctx, hc, md.DeviceEndpoint, form, &device); err != nil {
		return Token{}, fmt.Errorf("device login: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		return Token{}, errors.New("the device login response was incomplete")
	}
	fmt.Fprintf(opts.Out, "On any device, open:\n\n  %s\n\nsign in to Thetix (or create an account), and enter the code:\n\n  %s\n\n",
		device.VerificationURI, device.UserCode)
	if opts.OpenBrowser && device.VerificationURIComplete != "" {
		_ = OpenBrowser(device.VerificationURIComplete)
	}

	interval := time.Duration(max(device.Interval, 5)) * time.Second
	deadline := time.Now().Add(min(opts.Timeout, time.Duration(max(device.ExpiresIn, 60))*time.Second))
	for {
		if time.Now().Add(interval).After(deadline) {
			return Token{}, fmt.Errorf("the code was not entered in time; run `thetix login --device` again")
		}
		select {
		case <-ctx.Done():
			return Token{}, ctx.Err()
		case <-time.After(interval):
		}
		var token struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			Scope       string `json:"scope"`
		}
		poll := url.Values{"grant_type": {DeviceGrant}, "device_code": {device.DeviceCode}, "client_id": {clientID}}
		err := postForm(ctx, hc, md.TokenEndpoint, poll, &token)
		var oauthErr *OAuthError
		switch {
		case err == nil:
			if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") {
				return Token{}, errors.New("the token response had no bearer access token")
			}
			return Token{AccessToken: token.AccessToken, Scope: token.Scope}, nil
		case errors.As(err, &oauthErr) && oauthErr.Code == "authorization_pending":
		case errors.As(err, &oauthErr) && oauthErr.Code == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &oauthErr) && oauthErr.Code == "access_denied":
			return Token{}, errors.New("the connection was declined in Thetix")
		default:
			return Token{}, fmt.Errorf("device login: %w", err)
		}
	}
}

// Revoke revokes a token the server issued (RFC 7009).
func Revoke(ctx context.Context, hc *http.Client, server, token string) error {
	md, err := Discover(ctx, hc, server)
	if err != nil {
		return err
	}
	if md.RevocationEndpoint == "" {
		return errors.New("the server has no revocation endpoint")
	}
	return postForm(ctx, hc, md.RevocationEndpoint, url.Values{"token": {token}}, nil)
}

// OpenBrowser opens a URL in the person's browser, unless THETIX_NO_BROWSER is set.
func OpenBrowser(target string) error {
	if os.Getenv("THETIX_NO_BROWSER") != "" {
		return errors.New("THETIX_NO_BROWSER is set")
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}
