package bluesky

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// xrpc method names (the current official AT Protocol surface:
// com.atproto.* server and repo methods).
const (
	xrpcCreateSession  = "com.atproto.server.createSession"
	xrpcRefreshSession = "com.atproto.server.refreshSession"
	xrpcCreateRecord   = "com.atproto.repo.createRecord"
)

// maxResponseBytes bounds xrpc response bodies (a post-creation response is
// a few hundred bytes; anything larger is a misbehaving server).
const maxResponseBytes = 1 << 20

// session is the live AT Protocol session state for one account.
//
// SECURITY: the access and refresh JWTs exist only in this struct's memory
// for the lifetime of the process. They are never written to disk, logs,
// receipts, or error messages: the only places they appear in traffic are
// the authenticated request body/header, and every error surface runs
// through run.RedactSecrets.
type session struct {
	did     string
	handle  string
	access  string
	refresh string
}

// expired reports whether the access JWT has passed its exp claim.
//
// The JWT payload is decoded (not verified — the PDS verifies signatures)
// purely to know when the token dies, so a long thread can be rotated
// proactively. An unparseable JWT reports false and the server remains the
// authority: a 401 triggers a rotation.
func (s *session) expired(now time.Time) bool {
	if s.access == "" {
		return true
	}
	parts := strings.Split(s.access, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == nil {
		return false
	}
	return now.UTC().Unix() >= *claims.Exp
}

// sessionProvider is the authentication mechanism behind the publisher.
//
// This is the swap point for a future OAuth implementation: the publish
// pipeline only ever asks "give me a usable session for the configured
// account" and never sees raw tokens, so the mechanism can change without
// touching it.
type sessionProvider interface {
	// Session returns a usable session, performing a login or rotation
	// over the network when the in-memory state is absent or expired.
	Session(ctx context.Context) (*session, error)

	// Refresh force-rotates the session (called after the server rejects
	// the access JWT with 401).
	Refresh(ctx context.Context) (*session, error)

	// Forget discards the in-memory session state.
	Forget()
}

// passwordSessionProvider authenticates with the account's identifier and
// app password via com.atproto.server.createSession and rotates the access
// JWT via com.atproto.server.refreshSession.
type passwordSessionProvider struct {
	endpoint string
	client   *http.Client
	handle   string
	password string // in memory only — never persisted, logged, or embedded in errors

	mu      sync.Mutex
	current *session
}

func newPasswordSessionProvider(cfg resolvedConfig, client *http.Client) *passwordSessionProvider {
	return &passwordSessionProvider{
		endpoint: cfg.endpoint,
		client:   client,
		handle:   cfg.handle,
		password: os.Getenv(cfg.apiKeyEnv), // read once at construction; in memory only
	}
}

// Session implements sessionProvider.
//
// It returns a session whose access JWT is not expired, trying (in order)
// the existing refresh JWT, then a full login. A mechanism that returns an
// already-expired token is rotated once with its own refresh JWT before
// being abandoned.
func (p *passwordSessionProvider) Session(ctx context.Context) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if s := p.current; s != nil && !s.expired(time.Now()) {
		return s, nil
	}

	mechanisms := []func() (*session, error){}
	if cur := p.current; cur != nil && cur.refresh != "" {
		mechanisms = append(mechanisms, func() (*session, error) { return p.refreshLocked(ctx, cur) })
	}
	mechanisms = append(mechanisms, func() (*session, error) { return p.loginLocked(ctx) })

	var lastErr error
	for _, m := range mechanisms {
		s, err := m()
		if err != nil {
			lastErr = err
			continue
		}
		if s.expired(time.Now()) && s.refresh != "" {
			if s2, rerr := p.refreshLocked(ctx, s); rerr == nil && !s2.expired(time.Now()) {
				s = s2
			} else if lastErr == nil {
				lastErr = rerr
			}
		}
		if !s.expired(time.Now()) {
			p.current = s
			return s, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("session tokens are already expired")
		}
	}
	p.current = nil
	return nil, fmt.Errorf("%w: no usable session could be established: %v", ErrSession, lastErr)
}

// Refresh implements sessionProvider (called after the server rejects the
// access JWT with 401): rotate with the refresh JWT, falling back to a
// full login.
func (p *passwordSessionProvider) Refresh(ctx context.Context) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	if cur := p.current; cur != nil && cur.refresh != "" {
		next, err := p.refreshLocked(ctx, cur)
		if err == nil && !next.expired(time.Now()) {
			p.current = next
			return next, nil
		}
		lastErr = err
	}
	next, err := p.loginLocked(ctx)
	if err != nil || next.expired(time.Now()) {
		if lastErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrSession, lastErr)
		}
		return nil, fmt.Errorf("%w: %v", ErrSession, err)
	}
	p.current = next
	return next, nil
}

// Forget implements sessionProvider.
func (p *passwordSessionProvider) Forget() {
	p.mu.Lock()
	p.current = nil
	p.mu.Unlock()
}

// loginLocked performs createSession. Caller holds p.mu.
func (p *passwordSessionProvider) loginLocked(ctx context.Context) (*session, error) {
	var resp createSessionResponse
	err := xrpcDo(ctx, p.endpoint, p.client, xrpcCreateSession, createSessionRequest{
		Identifier: p.handle,
		Password:   p.password,
	}, &resp, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSession, err)
	}
	return p.sessionFromResponse(resp)
}

// refreshLocked rotates an existing session with its refresh JWT.
// Caller holds p.mu.
func (p *passwordSessionProvider) refreshLocked(ctx context.Context, cur *session) (*session, error) {
	var resp createSessionResponse
	err := xrpcDo(ctx, p.endpoint, p.client, xrpcRefreshSession, refreshSessionRequest{
		RefreshJwt: cur.refresh,
	}, &resp, "")
	if err != nil {
		return nil, err
	}
	return p.sessionFromResponse(resp)
}

// sessionFromResponse validates a session response and keeps the account
// facts plus the in-memory tokens.
func (p *passwordSessionProvider) sessionFromResponse(resp createSessionResponse) (*session, error) {
	if strings.TrimSpace(resp.DID) == "" || strings.TrimSpace(resp.Handle) == "" {
		return nil, fmt.Errorf("%w: session response is missing the account DID or handle", ErrSession)
	}
	if strings.TrimSpace(resp.AccessJWT) == "" {
		return nil, fmt.Errorf("%w: session response is missing the access JWT", ErrSession)
	}
	return &session{
		did:     strings.TrimSpace(resp.DID),
		handle:  strings.TrimSpace(resp.Handle),
		access:  resp.AccessJWT,
		refresh: strings.TrimSpace(resp.RefreshJWT),
	}, nil
}

// xrpcDo performs one xrpc POST against the given endpoint.
//
// in is marshalled as the JSON request body; out receives the JSON response
// body on a 2xx. bearer is the Authorization bearer token ("" for
// unauthenticated calls such as createSession); tokens never appear in
// errors.
//
// On a non-2xx response the xrpc error envelope ({"error": code,
// "message": ...}) is decoded and returned redacted; on a 2xx with a
// non-JSON body a decode error is returned. The returned error is always
// safe to persist or log.
func xrpcDo(ctx context.Context, endpoint string, client *http.Client, method string, in, out any, bearer string) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode xrpc request: %s", run.RedactSecrets(err.Error()))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/xrpc/"+method, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build xrpc request: %s", run.RedactSecrets(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "local-newsroom/"+clientVersion)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := client.Do(req)
	if err != nil {
		// Transport errors carry only the URL and the OS error — no
		// headers, no body — but redact anyway for the invariant.
		return fmt.Errorf("xrpc %s failed: %s", method, run.RedactSecrets(err.Error()))
	}
	defer func() {
		// Response bodies are always fully read before this point, so a
		// close failure (e.g. a torn keep-alive connection) cannot hide
		// a transport problem with the payload — note it anyway to keep
		// the deferred call lint-clean.
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("xrpc %s: reading the response failed: %s", method, run.RedactSecrets(err.Error()))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(method, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("xrpc %s: the 2xx response is not valid JSON: %s", method, run.RedactSecrets(err.Error()))
		}
	}
	return nil
}

// xrpcError is a non-2xx xrpc response. It is safe to persist and log: the
// server-authored envelope fields are redacted, and the HTTP status is
// structured (not parsed back out of a string), so callers can react to it
// (e.g. a 401 rotates the session).
type xrpcError struct {
	method  string
	status  int
	code    string // redacted envelope code ("")
	message string // redacted envelope message ("")
}

func (e *xrpcError) Error() string {
	if e.code == "" {
		return fmt.Sprintf("xrpc %s responded with HTTP %d (no error envelope)", e.method, e.status)
	}
	return fmt.Sprintf("xrpc %s responded with HTTP %d: %s: %s", e.method, e.status, e.code, e.message)
}

// IsUnauthorized reports whether the server rejected the credentials.
func (e *xrpcError) IsUnauthorized() bool {
	return e.status == http.StatusUnauthorized
}

// apiError maps a non-2xx xrpc response to an xrpcError.
//
// The xrpc error envelope is {"error": "<Code>", "message": "<text>"}; both
// are server-authored and are redacted before entering the error, so a
// malicious or buggy server cannot smuggle a token into a persisted error
// summary. Bodies without a usable envelope yield a status-only error.
func apiError(method string, status int, data []byte) *xrpcError {
	e := &xrpcError{method: method, status: status}
	var env struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if len(data) > 0 && json.Unmarshal(data, &env) == nil && env.Error != "" {
		e.code = run.RedactSecrets(env.Error)
		e.message = run.RedactSecrets(env.Message)
	}
	return e
}

// isUnauthorized reports whether err is a 401 xrpc response.
func isUnauthorized(err error) bool {
	var xe *xrpcError
	return errors.As(err, &xe) && xe.IsUnauthorized()
}

// defaultClient builds the production HTTP client with bounded behavior.
func defaultClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		// No redirect following: an xrpc endpoint that redirects is
		// misconfigured, and following it could carry the Authorization
		// header to another host.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errors.New("redirects are not followed for xrpc calls")
		},
	}
}
