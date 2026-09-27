package bullhorn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httpclient "github.com/bruin-data/ingestr/pkg/http"
	"github.com/bruin-data/ingestr/pkg/source"
)

func validQuery() string {
	return "client_id=id&client_secret=secret&refresh_token=rt&data_center=east&rate_limit=20&token_output=/tmp/bullhorn.token"
}

func TestParseURI(t *testing.T) {
	t.Run("valid with refresh token", func(t *testing.T) {
		creds, err := parseURI("bullhorn://?" + validQuery())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if creds.clientID != "id" || creds.clientSecret != "secret" || creds.refreshToken != "rt" ||
			creds.dataCenter != "east" || creds.rateLimit != 20 || creds.tokenOutput != "/tmp/bullhorn.token" {
			t.Fatalf("unexpected credentials: %+v", creds)
		}
	})

	t.Run("valid with username and password", func(t *testing.T) {
		_, err := parseURI("bullhorn://?client_id=id&client_secret=secret&username=u&password=p&data_center=east&rate_limit=20&token_output=/tmp/t")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("wrong scheme", func(t *testing.T) {
		_, err := parseURI("postgres://?" + validQuery())
		if err == nil {
			t.Fatal("expected error for wrong scheme")
		}
	})

	cases := []struct {
		name    string
		mutator func(string) string
	}{
		{"missing client_id", func(q string) string { return removeParam(q, "client_id") }},
		{"missing client_secret", func(q string) string { return removeParam(q, "client_secret") }},
		{"missing data_center", func(q string) string { return removeParam(q, "data_center") }},
		{"missing rate_limit", func(q string) string { return removeParam(q, "rate_limit") }},
		{"missing token_output", func(q string) string { return removeParam(q, "token_output") }},
		{"missing refresh_token and username/password", func(q string) string { return removeParam(q, "refresh_token") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseURI("bullhorn://?" + c.mutator(validQuery()))
			if err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}

	t.Run("non-numeric rate limit", func(t *testing.T) {
		_, err := parseURI("bullhorn://?" + removeParam(validQuery(), "rate_limit") + "&rate_limit=abc")
		if err == nil {
			t.Fatal("expected error for non-numeric rate_limit")
		}
	})

	t.Run("zero rate limit", func(t *testing.T) {
		_, err := parseURI("bullhorn://?" + removeParam(validQuery(), "rate_limit") + "&rate_limit=0")
		if err == nil {
			t.Fatal("expected error for zero rate_limit")
		}
	})
}

// removeParam strips one key=value pair from a raw query string built by
// validQuery, for table-driven "missing required field" tests.
func removeParam(q, key string) string {
	parts := []string{}
	for _, kv := range splitAmp(q) {
		if len(kv) >= len(key)+1 && kv[:len(key)+1] == key+"=" {
			continue
		}
		parts = append(parts, kv)
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "&"
		}
		out += p
	}
	return out
}

func splitAmp(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '&' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func TestIsValidTable(t *testing.T) {
	for table := range builtinEntities {
		if !isValidTable(table) {
			t.Errorf("expected %q to be a valid table", table)
		}
	}
	for _, bad := range []string{"", "Candidate", "unknown_table"} {
		if isValidTable(bad) {
			t.Errorf("expected %q to be invalid", bad)
		}
	}
}

func TestDiscoverDataCenter_FollowsRedirect(t *testing.T) {
	body := `{"oauthUrl":"https://auth-east.bullhornstaffing.com/oauth","restUrl":"https://rest-east.bullhornstaffing.com/rest-services"}`

	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer final.Close()

	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/rest-services/loginInfo", http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()

	old := loginInfoURL
	loginInfoURL = redirecting.URL
	defer func() { loginInfoURL = old }()

	dc, err := discoverDataCenter(context.Background(), "api_user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dc.name != "east" {
		t.Fatalf("expected data centre 'east', got %q", dc.name)
	}
	if dc.oauthURL != "https://auth-east.bullhornstaffing.com/oauth" {
		t.Fatalf("unexpected oauthURL: %s", dc.oauthURL)
	}
}

func TestDataCenterNameFromHost(t *testing.T) {
	cases := map[string]string{
		"https://auth-east.bullhornstaffing.com/oauth":  "east",
		"https://rest-east2.bullhornstaffing.com/x":     "east2",
		"https://auth-west1.bullhornstaffing.com/oauth": "west1",
	}
	for in, want := range cases {
		got, err := dataCenterNameFromHost(in)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", in, err)
		}
		if got != want {
			t.Errorf("dataCenterNameFromHost(%s) = %q, want %q", in, got, want)
		}
	}
}

// newTestSource builds a BullhornSource with its session/client wired to a
// test server, without going through the full OAuth/REST-login flow.
func newTestSource(t *testing.T, serverURL string) *BullhornSource {
	t.Helper()
	s := NewBullhornSource()
	s.session = &sessionState{}
	s.session.set("test-token", serverURL)
	s.oauth = &oauthState{}
	s.client = httpclient.New(
		httpclient.WithBaseURL(serverURL),
		httpclient.WithAuth(&bullhornAuth{session: s.session}),
		httpclient.WithDisableRetry(),
	)
	t.Cleanup(func() { _ = s.client.Close() })
	return s
}

func TestDoRequest_401TriggersReauthThenSucceeds(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	reauthCalls := 0
	s.reauthenticateFn = func(ctx context.Context) error {
		reauthCalls++
		s.session.set("new-token", server.URL)
		return nil
	}

	resp, err := s.doRequest(context.Background(), "Candidate", "/search/Candidate", func() (*httpclient.Response, error) {
		return s.client.R(context.Background()).Get("/search/Candidate")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsSuccess() {
		t.Fatalf("expected success, got status %d", resp.StatusCode())
	}
	if calls != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", calls)
	}
	if reauthCalls != 1 {
		t.Fatalf("expected exactly 1 re-authentication, got %d", reauthCalls)
	}
}

func TestDoRequest_401TwiceFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	reauthCalls := 0
	s.reauthenticateFn = func(ctx context.Context) error {
		reauthCalls++
		return nil
	}

	_, err := s.doRequest(context.Background(), "Candidate", "/search/Candidate", func() (*httpclient.Response, error) {
		return s.client.R(context.Background()).Get("/search/Candidate")
	})
	if err == nil {
		t.Fatal("expected an error after a second consecutive 401")
	}
	if reauthCalls != 1 {
		t.Fatalf("expected re-authentication to be attempted exactly once, got %d", reauthCalls)
	}
}

func TestDoRequest_412ReturnsDistinctError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	_, err := s.doRequest(context.Background(), "Candidate", "/search/Candidate", func() (*httpclient.Response, error) {
		return s.client.R(context.Background()).Get("/search/Candidate")
	})
	if err == nil {
		t.Fatal("expected an error for 412")
	}
	if calls != 1 {
		t.Fatalf("expected no retry on 412, got %d calls", calls)
	}
}

func TestDoRequest_429RetriesUncapped(t *testing.T) {
	old := retryWait429
	retryWait429 = time.Millisecond
	defer func() { retryWait429 = old }()

	const throttleCount = 12 // higher than any plausible bounded retry budget
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= throttleCount {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	resp, err := s.doRequest(context.Background(), "Candidate", "/search/Candidate", func() (*httpclient.Response, error) {
		return s.client.R(context.Background()).Get("/search/Candidate")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsSuccess() {
		t.Fatalf("expected eventual success, got %d", resp.StatusCode())
	}
	if calls != throttleCount+1 {
		t.Fatalf("expected %d calls, got %d", throttleCount+1, calls)
	}
	if s.CallCount() != 1 {
		t.Fatalf("expected call count to exclude throttled attempts, got %d", s.CallCount())
	}
}

func TestDoRequest_429HonoursCancellation(t *testing.T) {
	old := retryWait429
	retryWait429 = time.Second
	defer func() { retryWait429 = old }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := s.doRequest(ctx, "Candidate", "/search/Candidate", func() (*httpclient.Response, error) {
		return s.client.R(context.Background()).Get("/search/Candidate")
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error after cancellation")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected prompt cancellation, took %s", elapsed)
	}
}

func TestCallCount_IncrementsOnSuccessNotOn429(t *testing.T) {
	old := retryWait429
	retryWait429 = time.Millisecond
	defer func() { retryWait429 = old }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	for i := 0; i < 3; i++ {
		if _, err := s.doRequest(context.Background(), "Candidate", "/search/Candidate", func() (*httpclient.Response, error) {
			return s.client.R(context.Background()).Get("/search/Candidate")
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if s.CallCount() != 3 {
		t.Fatalf("expected call count 3, got %d", s.CallCount())
	}
}

// TestClose_WritesCallCountFile exercises the runner's integration contract:
// since the runner invokes ingestr as a subprocess, it cannot call CallCount()
// in-process and instead reads the file Close writes when AFC_CALL_COUNT_FILE
// is set. Close is called explicitly (unlike other tests, which leave closing
// to newTestSource's t.Cleanup), so the source is built inline here rather
// than via newTestSource: closing the underlying resty client twice panics.
func TestClose_WritesCallCountFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calls.txt")
	t.Setenv("AFC_CALL_COUNT_FILE", path)

	s := NewBullhornSource()
	s.client = httpclient.New(httpclient.WithBaseURL("http://unused.invalid"))
	s.callCount.Store(7)

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected call count file to be written: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "7" {
		t.Fatalf("expected call count file to contain 7, got %q", got)
	}
}

func TestClose_NoCallCountFileEnvVar_WritesNothing(t *testing.T) {
	t.Setenv("AFC_CALL_COUNT_FILE", "")
	s := NewBullhornSource()
	s.client = httpclient.New(httpclient.WithBaseURL("http://unused.invalid"))
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestObtainAccessToken_PersistsRefreshTokenBeforeRestLogin(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "bullhorn.token")

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh"}`))
	})
	mux.HandleFunc("/rest-services/login", func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(tokenPath)
		if err != nil || string(data) != "new-refresh" {
			t.Errorf("expected token_output to contain the rotated refresh token before REST login, got %q (err=%v)", data, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"BhRestToken":"session-token","restUrl":"` + r.Host + `/rest-services/e999/"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := NewBullhornSource()
	s.oauth = &oauthState{}
	s.creds = bullhornCredentials{
		clientID: "id", clientSecret: "secret", refreshToken: "old-refresh", tokenOutput: tokenPath,
	}
	dc := dataCenter{name: "east", oauthURL: server.URL, restURL: server.URL}

	token, err := s.obtainAccessToken(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "new-access" {
		t.Fatalf("expected new-access, got %q", token)
	}

	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("token file was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != tokenFilePerm {
		t.Fatalf("expected token file permissions %o, got %o", tokenFilePerm, perm)
	}
}

func TestPaginateAndSend_MultiPage(t *testing.T) {
	oldPageSize := defaultMaxPageSize
	defaultMaxPageSize = 2 // force a second fetch: a full first page implies more data
	defer func() { defaultMaxPageSize = oldPageSize }()

	pages := [][]map[string]any{
		{{"id": 1}, {"id": 2}},
		{{"id": 3}},
	}
	call := 0
	fetch := func(count, start int) ([]map[string]any, error) {
		if call >= len(pages) {
			return nil, nil
		}
		p := pages[call]
		call++
		return p, nil
	}

	s := NewBullhornSource()
	results := make(chan source.RecordBatchResult, 10)
	opts := source.ReadOptions{}
	if err := s.paginateAndSend(context.Background(), "candidate", []string{"id"}, nil, opts, results, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(results)

	var totalRows int64
	for r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected batch error: %v", r.Err)
		}
		totalRows += r.Batch.NumRows()
	}
	if totalRows != 3 {
		t.Fatalf("expected 3 rows total, got %d", totalRows)
	}
	if call != 2 {
		// a full page (== page size) continues; a short page (page 2, 1 < 2)
		// ends pagination without a further empty-page fetch.
		t.Fatalf("expected 2 fetch calls, got %d", call)
	}
}

func TestPaginateAndSend_StopsOnShortPage(t *testing.T) {
	// A page shorter than the (default) page size ends pagination without a
	// trailing empty-page fetch.
	fetch := func(count, start int) ([]map[string]any, error) {
		if start > 0 {
			t.Fatalf("did not expect a second page fetch, start=%d", start)
		}
		return []map[string]any{{"id": 1}}, nil
	}

	s := NewBullhornSource()
	results := make(chan source.RecordBatchResult, 10)
	if err := s.paginateAndSend(context.Background(), "candidate", []string{"id"}, nil, source.ReadOptions{}, results, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(results)

	count := 0
	for range results {
		count++
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 batch, got %d", count)
	}
}

func TestPaginateAndSend_RespectsMaxBatchBytes(t *testing.T) {
	fetch := func(count, start int) ([]map[string]any, error) {
		if start > 0 {
			return nil, nil
		}
		return []map[string]any{{"id": 1, "name": "a"}, {"id": 2, "name": "b"}, {"id": 3, "name": "c"}}, nil
	}

	s := NewBullhornSource()
	results := make(chan source.RecordBatchResult, 10)
	opts := source.ReadOptions{MaxBatchBytes: 1} // force a flush before every row
	if err := s.paginateAndSend(context.Background(), "candidate", []string{"id", "name"}, nil, opts, results, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(results)

	batches := 0
	var rows int64
	for r := range results {
		batches++
		rows += r.Batch.NumRows()
	}
	if batches < 2 {
		t.Fatalf("expected multiple batches with a tiny MaxBatchBytes, got %d", batches)
	}
	if rows != 3 {
		t.Fatalf("expected 3 rows total across batches, got %d", rows)
	}
}

func TestApplyAllowlist_DropsUnlistedFields(t *testing.T) {
	raw := map[string]any{"id": 1, "firstName": "A", "secret": "should not appear"}
	row := applyAllowlist(raw, []string{"id", "firstName"})
	if _, ok := row["secret"]; ok {
		t.Fatal("expected unlisted field to be dropped")
	}
	if row["id"] != 1 || row["firstName"] != "A" {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestConvertTimestampFields(t *testing.T) {
	row := map[string]any{
		"dateLastModified": float64(1495559294820),
		"dateAdded":        "1495559294820",
		"other":            "untouched",
	}
	convertTimestampFields(row, timestampFieldNames)

	got, ok := row["dateLastModified"].(time.Time)
	if !ok {
		t.Fatalf("expected dateLastModified to become time.Time, got %T", row["dateLastModified"])
	}
	want := time.UnixMilli(1495559294820).UTC()
	if !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	if _, ok := row["dateAdded"].(time.Time); !ok {
		t.Fatalf("expected dateAdded (string ms) to convert too, got %T", row["dateAdded"])
	}
	if row["other"] != "untouched" {
		t.Fatalf("expected non-timestamp field to be left alone")
	}
}

func TestJsonUseNumber(t *testing.T) {
	var v map[string]any
	// A value exceeding float64's exact-integer range (2^53) must round-trip
	// precisely as json.Number rather than losing precision as float64.
	if err := jsonUseNumber([]byte(`{"id": 9007199254740993}`), &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	n, ok := v["id"].(interface{ String() string })
	if !ok {
		t.Fatalf("expected json.Number, got %T", v["id"])
	}
	if n.String() != "9007199254740993" {
		t.Fatalf("expected exact precision, got %s", n.String())
	}
}

func TestValidateAllowlist(t *testing.T) {
	if err := validateAllowlist(nil); err == nil {
		t.Fatal("expected error for empty allowlist")
	}
	if err := validateAllowlist([]string{"*"}); err == nil {
		t.Fatal("expected error for wildcard allowlist")
	}
	if err := validateAllowlist([]string{"id", "firstName"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTable_RejectsMissingFields(t *testing.T) {
	s := NewBullhornSource()
	_, err := s.GetTable(context.Background(), source.TableRequest{Name: "candidate"})
	if err == nil {
		t.Fatal("expected error when no fields allowlist is given")
	}
}

func TestGetTable_RejectsWildcardFields(t *testing.T) {
	s := NewBullhornSource()
	_, err := s.GetTable(context.Background(), source.TableRequest{Name: "candidate?fields=*"})
	if err == nil {
		t.Fatal("expected error for wildcard fields")
	}
}

func TestGetTable_BuiltinTable(t *testing.T) {
	s := NewBullhornSource()
	table, err := s.GetTable(context.Background(), source.TableRequest{Name: "candidate?fields=id,firstName,dateLastModified"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if table.Name() != "candidate" {
		t.Fatalf("unexpected table name: %s", table.Name())
	}
	if table.IncrementalKey() != "dateLastModified" {
		t.Fatalf("unexpected incremental key: %s", table.IncrementalKey())
	}
	if len(table.PrimaryKeys()) != 1 || table.PrimaryKeys()[0] != "id" {
		t.Fatalf("unexpected primary keys: %v", table.PrimaryKeys())
	}
	if table.HasKnownSchema() {
		t.Fatal("expected schema inference (HasKnownSchema=false)")
	}
}

func TestGetTable_AdHocQueryEntity(t *testing.T) {
	s := NewBullhornSource()
	table, err := s.GetTable(context.Background(), source.TableRequest{Name: "tearsheet?entity=Tearsheet&fields=id,name,dateLastModified"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if table.Name() != "tearsheet" {
		t.Fatalf("unexpected table name: %s", table.Name())
	}
}

func TestGetTable_UnknownTableWithoutEntity(t *testing.T) {
	s := NewBullhornSource()
	_, err := s.GetTable(context.Background(), source.TableRequest{Name: "unknown_table?fields=id"})
	if err == nil {
		t.Fatal("expected error for an unknown table with no entity= override")
	}
}

func TestReadMeta_ReturnsFieldDefinitionsNotData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fields") != "*" {
			t.Errorf("expected fields=* on the meta call, got %q", r.URL.Query().Get("fields"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entity":"Candidate","fields":[{"name":"id","type":"ID","dataType":"Integer","label":"ID"}]}`))
	}))
	defer server.Close()

	s := newTestSource(t, server.URL)
	results := make(chan source.RecordBatchResult, 4)
	err := s.readMeta(context.Background(), []string{"Candidate"}, source.ReadOptions{}, results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(results)

	var rows int64
	for r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected error in batch: %v", r.Err)
		}
		rows += r.Batch.NumRows()
	}
	if rows != 1 {
		t.Fatalf("expected 1 metadata row, got %d", rows)
	}
}

func TestReadMeta_RequiresEntities(t *testing.T) {
	s := NewBullhornSource()
	err := s.readMeta(context.Background(), nil, source.ReadOptions{}, make(chan source.RecordBatchResult, 1))
	if err == nil {
		t.Fatal("expected error when no entities are given")
	}
}

func TestReadSubscription_TracksRequestID(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	var sawRequestID string
	mux := http.NewServeMux()
	mux.HandleFunc("/event/subscription/ingestr", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		sawRequestID = r.URL.Query().Get("requestId")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":42,"events":[
			{"eventId":"e1","eventTimestamp":1495559294820,"entityName":"Candidate","entityId":1,"entityEventType":"UPDATED"},
			{"eventId":"e2","eventTimestamp":1495559300000,"entityName":"Candidate","entityId":2,"entityEventType":"DELETED"}
		]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := newTestSource(t, server.URL)
	results := make(chan source.RecordBatchResult, 4)
	params := tableParams{Entities: []string{"Candidate"}, StatePath: statePath}
	if err := s.readSubscription(context.Background(), params, source.ReadOptions{}, results); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(results)

	if sawRequestID != "" {
		t.Fatalf("expected no requestId on first poll, got %q", sawRequestID)
	}

	var rows int64
	for r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected error: %v", r.Err)
		}
		rows += r.Batch.NumRows()
	}
	if rows != 1 {
		t.Fatalf("expected only the DELETED event as a row, got %d", rows)
	}

	got, err := loadSubscriptionState(statePath)
	if err != nil {
		t.Fatalf("failed to read persisted state: %v", err)
	}
	if got != 42 {
		t.Fatalf("expected persisted requestId 42, got %d", got)
	}
}

func TestReadSubscription_ResumesFromPersistedRequestID(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveSubscriptionState(statePath, 41); err != nil {
		t.Fatalf("failed to seed state: %v", err)
	}

	var sawRequestID string
	mux := http.NewServeMux()
	mux.HandleFunc("/event/subscription/ingestr", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		sawRequestID = r.URL.Query().Get("requestId")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":43,"events":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := newTestSource(t, server.URL)
	params := tableParams{Entities: []string{"Candidate"}, StatePath: statePath}
	if err := s.readSubscription(context.Background(), params, source.ReadOptions{}, make(chan source.RecordBatchResult, 4)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawRequestID != "41" {
		t.Fatalf("expected the poll to resume from the persisted requestId 41, got %q", sawRequestID)
	}
}
