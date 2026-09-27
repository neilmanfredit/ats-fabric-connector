package bullhorn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bruin-data/ingestr/internal/config"
	httpclient "github.com/bruin-data/ingestr/pkg/http"
	"resty.dev/v3"
)

const (
	// tokenOutput and subscription state files are written for the owning
	// user only; they hold live session/refresh credentials.
	tokenFilePerm = 0o600
)

// loginInfoURL is a var (not a const) so tests can point it at a local
// httptest server instead of the real Bullhorn endpoint.
var loginInfoURL = "https://rest.bullhornstaffing.com/rest-services/loginInfo"

// dataCenter holds the hosts returned by loginInfo for a given username.
type dataCenter struct {
	name     string // e.g. "east", "east2" — the swimlane discovered from the response host
	oauthURL string
	restURL  string
}

// discoverDataCenter calls loginInfo (following the documented 307 redirect)
// to find the OAuth and REST hosts for a username (brief §6.1.1). The
// underlying HTTP client follows redirects (including 307, which preserves
// the request method) by default, so no special redirect handling is needed
// here.
func discoverDataCenter(ctx context.Context, username string) (dataCenter, error) {
	client := httpclient.New(httpclient.WithTimeout(30 * time.Second))
	defer func() { _ = client.Close() }()

	resp, err := client.R(ctx).
		SetQueryParam("username", username).
		Get(loginInfoURL)
	if err != nil {
		return dataCenter{}, fmt.Errorf("loginInfo request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return dataCenter{}, fmt.Errorf("loginInfo returned status %d: %s", resp.StatusCode(), resp.String())
	}

	var body struct {
		OauthURL string `json:"oauthUrl"`
		RestURL  string `json:"restUrl"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return dataCenter{}, fmt.Errorf("failed to parse loginInfo response: %w", err)
	}
	if body.OauthURL == "" || body.RestURL == "" {
		return dataCenter{}, fmt.Errorf("loginInfo response missing oauthUrl/restUrl")
	}

	name, err := dataCenterNameFromHost(body.OauthURL)
	if err != nil {
		return dataCenter{}, err
	}

	return dataCenter{name: name, oauthURL: strings.TrimRight(body.OauthURL, "/"), restURL: strings.TrimRight(body.RestURL, "/")}, nil
}

// dataCenterNameFromHost extracts the data-centre code from a host like
// "auth-east.bullhornstaffing.com" -> "east".
func dataCenterNameFromHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid data centre URL %q: %w", rawURL, err)
	}
	host := u.Hostname()
	parts := strings.SplitN(host, ".", 2)
	if len(parts) == 0 {
		return "", fmt.Errorf("cannot determine data centre from host %q", host)
	}
	name := strings.TrimPrefix(parts[0], "auth-")
	name = strings.TrimPrefix(name, "rest-")
	if name == "" {
		return "", fmt.Errorf("cannot determine data centre from host %q", host)
	}
	return name, nil
}

// sessionState holds the live REST session, guarded for concurrent access
// from the Authenticator applied to every outgoing request.
type sessionState struct {
	mu          sync.Mutex
	bhRestToken string
	restURL     string
}

func (s *sessionState) set(token, restURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bhRestToken = token
	s.restURL = restURL
}

func (s *sessionState) getToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bhRestToken
}

func (s *sessionState) getRestURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restURL
}

// oauthState holds the current OAuth access/refresh tokens.
type oauthState struct {
	mu           sync.Mutex
	accessToken  string
	refreshToken string
}

// bullhornAuth applies the current BhRestToken header to every request. It
// reads live state on each call so a mid-run token rotation (401 refresh)
// takes effect without rebuilding the HTTP client.
type bullhornAuth struct {
	session *sessionState
}

func (a *bullhornAuth) Apply(req *resty.Request) error {
	req.SetHeader("BhRestToken", a.session.getToken())
	return nil
}

func (a *bullhornAuth) Name() string { return "bullhorn-session" }

// authenticate obtains a REST session, minting an access token from the
// refresh token or the authorization-code flow first if needed.
func (s *BullhornSource) authenticate(ctx context.Context, dc dataCenter) error {
	accessToken, err := s.obtainAccessToken(ctx, dc)
	if err != nil {
		return err
	}
	bhRestToken, restURL, err := restLogin(ctx, dc.restURL, accessToken)
	if err != nil {
		return fmt.Errorf("bullhorn REST login failed: %w", err)
	}
	s.session.set(bhRestToken, restURL)
	return nil
}

// obtainAccessToken mints an OAuth access token from a refresh token, or —
// when only username/password are given — the authorization-code flow
// (brief §6.1.2/§6.1.3). The API user must have accepted Bullhorn's Terms of
// Service once, manually, before the authorization-code flow succeeds.
func (s *BullhornSource) obtainAccessToken(ctx context.Context, dc dataCenter) (string, error) {
	if s.creds.refreshToken != "" {
		access, newRefresh, err := refreshAccessToken(ctx, dc.oauthURL, s.creds.clientID, s.creds.clientSecret, s.creds.refreshToken)
		if err != nil {
			return "", fmt.Errorf("failed to refresh bullhorn access token: %w", err)
		}
		// Persist the new refresh token before it is used for any request:
		// Bullhorn invalidates the previous token on every rotation (brief
		// §6.1.4.1), so a crash between use and persistence would strand us.
		if err := persistToken(s.creds.tokenOutput, newRefresh); err != nil {
			return "", fmt.Errorf("failed to persist rotated refresh token: %w", err)
		}
		s.creds.refreshToken = newRefresh
		s.oauth.mu.Lock()
		s.oauth.accessToken = access
		s.oauth.refreshToken = newRefresh
		s.oauth.mu.Unlock()
		return access, nil
	}

	code, err := requestAuthorizationCode(ctx, dc.oauthURL, s.creds.clientID, s.creds.username, s.creds.password)
	if err != nil {
		return "", fmt.Errorf("failed to obtain bullhorn authorization code: %w", err)
	}
	access, refresh, err := exchangeAuthorizationCode(ctx, dc.oauthURL, s.creds.clientID, s.creds.clientSecret, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange bullhorn authorization code: %w", err)
	}
	if err := persistToken(s.creds.tokenOutput, refresh); err != nil {
		return "", fmt.Errorf("failed to persist bullhorn refresh token: %w", err)
	}
	s.creds.refreshToken = refresh
	s.oauth.mu.Lock()
	s.oauth.accessToken = access
	s.oauth.refreshToken = refresh
	s.oauth.mu.Unlock()
	return access, nil
}

// requestAuthorizationCode drives the resource-owner password step of the
// authorization-code flow (brief §6.1.2). Bullhorn returns the code via a
// redirect Location query parameter.
func requestAuthorizationCode(ctx context.Context, oauthURL, clientID, username, password string) (string, error) {
	client := httpclient.New(httpclient.WithTimeout(30 * time.Second))
	defer func() { _ = client.Close() }()

	resp, err := client.R(ctx).
		SetQueryParam("client_id", clientID).
		SetQueryParam("response_type", "code").
		SetQueryParam("action", "Login").
		SetQueryParam("username", username).
		SetQueryParam("password", password).
		SetQueryParam("state", "bullhorn-ingestr").
		Get(oauthURL + "/oauth/authorize")
	if err != nil {
		return "", fmt.Errorf("authorization request failed: %w", err)
	}

	// Live behaviour may return the code via a redirect Location header (not
	// followed, since it points at a non-API callback URL) or inline in the
	// body; both shapes are supported, see fabric/REPORT.md for the
	// runbook-confirmed outcome.
	if loc := resp.Header().Get("Location"); loc != "" {
		if code := extractCodeParam(loc); code != "" {
			return code, nil
		}
	}
	if code := extractCodeParam(string(resp.Body())); code != "" {
		return code, nil
	}
	return "", fmt.Errorf("authorization response did not contain a code (status %d)", resp.StatusCode())
}

func extractCodeParam(s string) string {
	if idx := strings.Index(s, "code="); idx >= 0 {
		rest := s[idx+len("code="):]
		if amp := strings.IndexAny(rest, "&\"' \n"); amp >= 0 {
			rest = rest[:amp]
		}
		if code, err := url.QueryUnescape(rest); err == nil {
			return code
		}
		return rest
	}
	return ""
}

// exchangeAuthorizationCode exchanges an authorization code for an access
// and refresh token (brief §6.1.3).
func exchangeAuthorizationCode(ctx context.Context, oauthURL, clientID, clientSecret, code string) (accessToken, refreshToken string, err error) {
	return postOAuthToken(ctx, oauthURL, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"client_id":     clientID,
		"client_secret": clientSecret,
	})
}

// refreshAccessToken exchanges a refresh token for a new access token and a
// new refresh token; Bullhorn invalidates the previous refresh token on
// every rotation (brief §6.1.4).
func refreshAccessToken(ctx context.Context, oauthURL, clientID, clientSecret, refreshToken string) (accessToken, newRefreshToken string, err error) {
	return postOAuthToken(ctx, oauthURL, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"client_secret": clientSecret,
	})
}

func postOAuthToken(ctx context.Context, oauthURL string, form map[string]string) (accessToken, refreshToken string, err error) {
	client := httpclient.New(
		httpclient.WithTimeout(30*time.Second),
		httpclient.WithAllowNonIdempotentRetry(),
	)
	defer func() { _ = client.Close() }()

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	resp, err := client.R(ctx).
		SetFormData(form).
		SetResult(&tokenResp).
		Post(oauthURL + "/oauth/token")
	if err != nil {
		return "", "", fmt.Errorf("token request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return "", "", fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode(), resp.String())
	}
	if tokenResp.AccessToken == "" {
		return "", "", fmt.Errorf("token response did not contain an access_token")
	}
	if tokenResp.RefreshToken == "" {
		return "", "", fmt.Errorf("token response did not contain a refresh_token")
	}
	return tokenResp.AccessToken, tokenResp.RefreshToken, nil
}

// restLogin exchanges an OAuth access token for a Bullhorn REST session
// (brief §6.1.5). The returned restURL becomes the base URL for every
// subsequent API call.
func restLogin(ctx context.Context, restBaseURL, accessToken string) (bhRestToken, restURL string, err error) {
	client := httpclient.New(httpclient.WithTimeout(30 * time.Second))
	defer func() { _ = client.Close() }()

	var loginResp struct {
		BhRestToken string `json:"BhRestToken"`
		RestURL     string `json:"restUrl"`
	}
	resp, err := client.R(ctx).
		SetQueryParam("version", "*").
		SetQueryParam("access_token", accessToken).
		SetResult(&loginResp).
		Post(restBaseURL + "/rest-services/login")
	if err != nil {
		return "", "", fmt.Errorf("REST login request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return "", "", fmt.Errorf("REST login returned status %d: %s", resp.StatusCode(), resp.String())
	}
	if loginResp.BhRestToken == "" || loginResp.RestURL == "" {
		return "", "", fmt.Errorf("REST login response missing BhRestToken/restUrl")
	}
	return loginResp.BhRestToken, strings.TrimRight(loginResp.RestURL, "/"), nil
}

// reauthenticate re-runs the token refresh and REST login, updating the
// shared session in place. Called only from the 401 path in doRequest — the
// session is otherwise reused for the lifetime of the connection (brief
// §6.1.6.1).
func (s *BullhornSource) reauthenticate(ctx context.Context) error {
	dc, err := discoverDataCenter(ctx, s.creds.username)
	if err != nil {
		return fmt.Errorf("re-authentication data centre lookup failed: %w", err)
	}
	return s.authenticate(ctx, dc)
}

// persistToken writes a rotated token to disk atomically (write to a temp
// file, then rename) with owner-only permissions (brief §6.3.3). It never
// logs the token value.
func persistToken(path, token string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bullhorn-token-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary token file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup; no-op once renamed

	if err := tmp.Chmod(tokenFilePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set token file permissions: %w", err)
	}
	if _, err := tmp.WriteString(token); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close token file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to move token file into place: %w", err)
	}
	return nil
}

// doRequest executes an HTTP call with Bullhorn's session semantics: 401
// triggers one re-authentication and retry, 412 means no session token was
// sent at all (a distinct, non-retryable error), and 429 waits and retries
// with no cap, honouring context cancellation, since throttled requests do
// not count against the monthly quota (brief §6.1.6.2, §6.1.13).
func (s *BullhornSource) doRequest(ctx context.Context, entity, endpoint string, do func() (*httpclient.Response, error)) (*httpclient.Response, error) {
	retried401 := false
	for {
		resp, err := do()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", entity, endpoint, err)
		}

		switch resp.StatusCode() {
		case http.StatusTooManyRequests:
			config.Debug("[BULLHORN] 429 on %s %s, waiting %s", entity, endpoint, retryWait429)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryWait429):
			}
			continue
		case http.StatusUnauthorized:
			if retried401 {
				return nil, fmt.Errorf("%s %s: session expired again immediately after refresh (401)", entity, endpoint)
			}
			config.Debug("[BULLHORN] 401 on %s %s, re-authenticating", entity, endpoint)
			reauth := s.reauthenticate
			if s.reauthenticateFn != nil {
				reauth = s.reauthenticateFn
			}
			if err := reauth(ctx); err != nil {
				return nil, fmt.Errorf("%s %s: re-authentication after 401 failed: %w", entity, endpoint, err)
			}
			retried401 = true
			continue
		case http.StatusPreconditionFailed:
			return nil, fmt.Errorf("%s %s: no session token was sent (412)", entity, endpoint)
		default:
			if !resp.IsSuccess() {
				return nil, fmt.Errorf("%s %s returned status %d: %s", entity, endpoint, resp.StatusCode(), resp.String())
			}
			s.callCount.Add(1)
			return resp, nil
		}
	}
}

// ensureSubscription creates the event subscription if it does not already
// exist. The call is idempotent, so re-issuing it on every run is safe (brief
// §6.1.11.1); one subscription comfortably covers the default table set,
// well under the 50-subscription cap (brief §6.1.11.3).
func (s *BullhornSource) ensureSubscription(ctx context.Context, subscriptionID string, entities []string) error {
	endpoint := "/event/subscription/" + subscriptionID
	_, err := s.doRequest(ctx, "subscription", endpoint, func() (*httpclient.Response, error) {
		return s.client.R(ctx).
			SetQueryParam("type", "entity").
			SetQueryParam("names", strings.Join(entities, ",")).
			SetQueryParam("eventTypes", "INSERTED,UPDATED,DELETED").
			Put(endpoint)
	})
	return err
}

type subscriptionState struct {
	LastRequestID int64 `json:"lastRequestId"`
}

func loadSubscriptionState(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var state subscriptionState
	if err := json.Unmarshal(data, &state); err != nil {
		return 0, fmt.Errorf("failed to parse subscription state file: %w", err)
	}
	return state.LastRequestID, nil
}

func saveSubscriptionState(path string, requestID int64) error {
	data, err := json.Marshal(subscriptionState{LastRequestID: requestID})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bullhorn-subscription-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup; no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close state file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to move state file into place: %w", err)
	}
	return nil
}
