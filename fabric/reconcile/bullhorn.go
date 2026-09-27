package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// BullhornCounter queries Bullhorn directly for reconciliation aggregates:
// totalOnly counts and ID-only key lists (build brief section 10).
//
// This is a compact, standalone OAuth/session client rather than a reuse of
// pkg/source/bullhorn/auth.go, since that package is not built to be
// imported as a library from fabric/. Once both are stable, consider
// factoring the shared session logic out (see fabric/REPORT.md deviations).
type BullhornCounter interface {
	TotalCount(ctx context.Context, entity, dateField string, since time.Time) (int64, error)
	ListIDs(ctx context.Context, entity string) ([]string, error)
}

type httpBullhornCounter struct {
	restURL     string
	bhRestToken string
	httpClient  *http.Client
}

// NewBullhornCounter runs the login sequence once (data-centre discovery,
// refresh-token exchange, REST login) and returns a session-bound counter.
func NewBullhornCounter(ctx context.Context, clientID, clientSecret, refreshToken, username, dataCenter string) (*httpBullhornCounter, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}

	dcHost, err := discoverDataCenter(ctx, httpClient, username)
	if err != nil {
		return nil, err
	}
	if dataCenter != "" && dcHost != dataCenter {
		return nil, fmt.Errorf("configured data_center %q does not match discovered %q", dataCenter, dcHost)
	}

	accessToken, err := exchangeRefreshToken(ctx, httpClient, dcHost, clientID, clientSecret, refreshToken)
	if err != nil {
		return nil, err
	}

	bhRestToken, restURL, err := restLogin(ctx, httpClient, dcHost, accessToken)
	if err != nil {
		return nil, err
	}

	return &httpBullhornCounter{restURL: restURL, bhRestToken: bhRestToken, httpClient: httpClient}, nil
}

func discoverDataCenter(ctx context.Context, client *http.Client, username string) (string, error) {
	loginInfoURL := "https://rest.bullhornstaffing.com/rest-services/loginInfo?username=" + url.QueryEscape(username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginInfoURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req) // http.Client follows the documented 307 automatically.
	if err != nil {
		return "", fmt.Errorf("loginInfo request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("loginInfo returned status %d", resp.StatusCode)
	}
	var out struct {
		OauthURL string `json:"oauthUrl"`
		RestURL  string `json:"restUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding loginInfo response: %w", err)
	}
	u, err := url.Parse(out.OauthURL)
	if err != nil {
		return "", fmt.Errorf("parsing oauthUrl %q: %w", out.OauthURL, err)
	}
	// Host looks like auth-<dc>.bullhornstaffing.com.
	host := u.Hostname()
	const prefix, suffix = "auth-", ".bullhornstaffing.com"
	if len(host) > len(prefix)+len(suffix) {
		return host[len(prefix) : len(host)-len(suffix)], nil
	}
	return "", fmt.Errorf("could not parse data centre from oauthUrl %q", out.OauthURL)
}

func exchangeRefreshToken(ctx context.Context, client *http.Client, dc, clientID, clientSecret, refreshToken string) (string, error) {
	tokenURL := fmt.Sprintf("https://auth-%s.bullhornstaffing.com/oauth/token", dc)
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		return "", err
	}
	req.URL.RawQuery = form.Encode()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth/token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth/token returned status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding oauth/token response: %w", err)
	}
	return out.AccessToken, nil
}

func restLogin(ctx context.Context, client *http.Client, dc, accessToken string) (bhRestToken, restURL string, err error) {
	loginURL := fmt.Sprintf(
		"https://rest-%s.bullhornstaffing.com/rest-services/login?version=*&access_token=%s",
		dc, url.QueryEscape(accessToken),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("rest login request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("rest login returned status %d", resp.StatusCode)
	}
	var out struct {
		BhRestToken string `json:"BhRestToken"`
		RestURL     string `json:"restUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decoding rest login response: %w", err)
	}
	return out.BhRestToken, out.RestURL, nil
}

// TotalCount asks Bullhorn for a totalOnly count of rows changed at or after
// `since` (build brief section 6.1.7.4). The exact JPQL date-comparison
// syntax is pending confirmation via fabric/verification/RUNBOOK.md.
func (c *httpBullhornCounter) TotalCount(ctx context.Context, entity, dateField string, since time.Time) (int64, error) {
	q := url.Values{
		"where":     {fmt.Sprintf("%s >= %d", dateField, since.UnixMilli())},
		"count":     {"1"},
		"totalOnly": {"true"},
	}
	reqURL := fmt.Sprintf("%s/query/%s?%s", c.restURL, entity, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("BhRestToken", c.bhRestToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("totalOnly query for %s: %w", entity, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("totalOnly query for %s returned status %d", entity, resp.StatusCode)
	}
	var out struct {
		Total int64 `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decoding totalOnly response for %s: %w", entity, err)
	}
	return out.Total, nil
}

// ListIDs fetches every id for an entity, paginated. Used only for the
// less-frequent key-comparison reconciliation pass (section 10.2), never for
// the daily count pass.
func (c *httpBullhornCounter) ListIDs(ctx context.Context, entity string) ([]string, error) {
	const pageSize = 500
	var ids []string
	start := 0
	for {
		q := url.Values{
			"where":  {"id IS NOT NULL"},
			"fields": {"id"},
			"count":  {strconv.Itoa(pageSize)},
			"start":  {strconv.Itoa(start)},
		}
		reqURL := fmt.Sprintf("%s/query/%s?%s", c.restURL, entity, q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("BhRestToken", c.bhRestToken)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("id query for %s: %w", entity, err)
		}
		var page struct {
			Data []struct {
				ID json.Number `json:"id"`
			} `json:"data"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("id query for %s returned status %d", entity, resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding id query response for %s: %w", entity, decodeErr)
		}
		for _, row := range page.Data {
			ids = append(ids, row.ID.String())
		}
		if len(page.Data) < pageSize {
			break
		}
		start += pageSize
	}
	return ids, nil
}
