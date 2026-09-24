// Package bullhorn implements an ingestr source for the Bullhorn REST API
// (ATS/CRM). It reads a fixed set of entities via Bullhorn's search and query
// endpoints, plus entity metadata and event-subscription-derived deletes.
//
// Every entity read requires an explicit field allowlist; wildcard field
// selection is not permitted (Bullhorn API Fair Use Policy data-minimisation
// requirement). Schema is inferred (KnownSchema: false) rather than
// hand-maintained, per the upstream add-source convention.
package bullhorn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bruin-data/ingestr/internal/config"
	"github.com/bruin-data/ingestr/pkg/arrowconv"
	httpclient "github.com/bruin-data/ingestr/pkg/http"
	"github.com/bruin-data/ingestr/pkg/schema"
	"github.com/bruin-data/ingestr/pkg/source"
	"github.com/bruin-data/ingestr/pkg/tablespec"
)

const (
	// Bullhorn: 1,500 requests/minute per OAuth client_id (B2). There is no
	// vendor-derived default rate limit (build brief §6.3.4) — rate_limit is
	// a required URI parameter, in requests/second.
	rateLimitBurst = 5

	// searchQueryLengthLimit is the point at which the GET form of
	// search/query must switch to POST (brief §6.1.7.3).
	searchQueryLengthLimit = 7500
)

// retryWait429 is a var (not a const) so tests can shrink it; production
// code always uses the 1-second wait mandated by B2 (brief §6.1.13).
// Bullhorn returns 429 with no documented retry cap; the response does not
// count against the monthly call quota.
var retryWait429 = 1 * time.Second

// defaultMaxPageSize is the default page size for search/query pagination
// (a var, not a const, so tests can shrink it). The exact per-endpoint
// maximum is a §6.2.3 runbook item; 500 is a conservative default.
var defaultMaxPageSize = 500

// endpointStyle selects which Bullhorn API shape an entity is read through.
type endpointStyle string

const (
	styleSearch       endpointStyle = "search"
	styleQuery        endpointStyle = "query"
	styleMeta         endpointStyle = "meta"
	styleSubscription endpointStyle = "subscription"
)

// entityConfig describes one Bronze table sourced from Bullhorn.
type entityConfig struct {
	table          string
	entity         string
	style          endpointStyle
	primaryKeys    []string
	incrementalKey string
	strategy       config.IncrementalStrategy
}

// builtinEntities is the default table set from build brief §6.4. Tables not
// listed here can still be read as ad hoc query-based entities by passing
// `entity=<BullhornEntity>` on the table string (see GetTable).
var builtinEntities = map[string]entityConfig{
	"candidate": {
		table: "candidate", entity: "Candidate", style: styleSearch,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"client_corporation": {
		table: "client_corporation", entity: "ClientCorporation", style: styleSearch,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"client_contact": {
		table: "client_contact", entity: "ClientContact", style: styleSearch,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"job_order": {
		table: "job_order", entity: "JobOrder", style: styleSearch,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"placement": {
		table: "placement", entity: "Placement", style: styleSearch,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"job_submission": {
		table: "job_submission", entity: "JobSubmission", style: styleQuery,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"corporate_user": {
		table: "corporate_user", entity: "CorporateUser", style: styleQuery,
		primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
	},
	"entity_metadata": {
		table: "entity_metadata", style: styleMeta,
		primaryKeys: []string{"entity", "field_name"}, strategy: config.StrategyReplace,
	},
	"deleted_records": {
		table: "deleted_records", style: styleSubscription,
		primaryKeys: []string{"entity", "entity_id", "event_id"}, incrementalKey: "event_timestamp", strategy: config.StrategyMerge,
	},
}

// timestampFieldNames are allowlisted fields always treated as Bullhorn
// epoch-millisecond timestamps and converted to time.Time. Additional fields
// can be flagged per read via the `timestamp_fields` table parameter.
var timestampFieldNames = []string{"dateAdded", "dateLastModified", "dateLastVisit", "startDate", "endDate", "dateBegin", "dateEnd"}

// BullhornSource implements source.Source against the Bullhorn REST API.
type BullhornSource struct {
	client    *httpclient.Client
	session   *sessionState
	oauth     *oauthState
	creds     bullhornCredentials
	entities  map[string]entityConfig
	callCount atomic.Int64

	// reauthenticateFn overrides doRequest's 401 handling in tests, isolating
	// retry-loop behaviour from the full OAuth/REST-login plumbing.
	// Production code leaves this nil and uses reauthenticate directly.
	reauthenticateFn func(ctx context.Context) error
}

func NewBullhornSource() *BullhornSource {
	entities := make(map[string]entityConfig, len(builtinEntities))
	for k, v := range builtinEntities {
		entities[k] = v
	}
	return &BullhornSource{entities: entities}
}

func (s *BullhornSource) Schemes() []string { return []string{"bullhorn"} }

func (s *BullhornSource) HandlesIncrementality() bool { return true }

// CallCount returns the number of Bullhorn API calls made since Connect,
// excluding 429 responses (which do not count against the monthly quota).
// The runner reads this after each entity run to maintain its month-to-date
// budget total (brief §6.6). In-process callers (tests, a future library
// integration) should call this directly; Close also writes it to
// callCountFileEnvVar for the common case where the runner invokes ingestr
// as a subprocess and has no other way to read it.
func (s *BullhornSource) CallCount() int64 { return s.callCount.Load() }

// callCountFileEnvVar, when set, names a file Close writes the call count to
// (as a bare integer). The runner sets this before invoking ingestr as a
// subprocess per entity, then reads the file back afterwards.
const callCountFileEnvVar = "AFC_CALL_COUNT_FILE"

func (s *BullhornSource) Connect(ctx context.Context, uri string) error {
	creds, err := parseURI(uri)
	if err != nil {
		return err
	}
	s.creds = creds

	dc, err := discoverDataCenter(ctx, creds.username)
	if err != nil {
		return fmt.Errorf("bullhorn data centre discovery failed: %w", err)
	}
	if !strings.EqualFold(dc.name, creds.dataCenter) {
		return fmt.Errorf("data_center %q does not match discovered data centre %q for user %q", creds.dataCenter, dc.name, creds.username)
	}

	s.oauth = &oauthState{}
	s.session = &sessionState{}

	if err := s.authenticate(ctx, dc); err != nil {
		return err
	}

	s.client = httpclient.New(
		httpclient.WithBaseURL(s.session.getRestURL()),
		httpclient.WithTimeout(60*time.Second),
		httpclient.WithRateLimiter(creds.rateLimit, rateLimitBurst),
		httpclient.WithDebug(config.DebugMode),
		httpclient.WithAuth(&bullhornAuth{session: s.session}),
		// doRequest owns 401/412/429 handling; the shared client must not
		// retry underneath it or the two loops fight over the same response.
		httpclient.WithDisableRetry(),
	)

	config.Debug("[BULLHORN] connected to data centre %s", dc.name)
	return nil
}

func (s *BullhornSource) Close(ctx context.Context) error {
	if path := os.Getenv(callCountFileEnvVar); path != "" {
		count := strconv.FormatInt(s.callCount.Load(), 10)
		if err := os.WriteFile(path, []byte(count), 0o600); err != nil {
			config.Debug("[BULLHORN] failed to write call count file %s: %v", path, err)
		}
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// tableParams are the query-string-style parameters accepted on a Bullhorn
// source table string, e.g. "candidate?fields=id,firstName,dateLastModified".
type tableParams struct {
	Fields          []string `mapstructure:"fields"`
	Entity          string   `mapstructure:"entity"`
	Entities        []string `mapstructure:"entities"`
	TimestampFields []string `mapstructure:"timestamp_fields"`
	SubscriptionID  string   `mapstructure:"subscription_id"`
	StatePath       string   `mapstructure:"state_path"`
}

func (s *BullhornSource) GetTable(ctx context.Context, req source.TableRequest) (source.SourceTable, error) {
	var params tableParams
	basePath, _, err := tablespec.Parse(req.Name, &params, tablespec.WithListSeparator(","))
	if err != nil {
		return nil, fmt.Errorf("bullhorn: invalid table parameters for %q: %w", req.Name, err)
	}

	cfg, ok := s.entities[basePath]
	if !ok {
		if params.Entity == "" {
			return nil, fmt.Errorf("bullhorn: unsupported table %q; add it with an explicit entity=<BullhornEntity> parameter", basePath)
		}
		cfg = entityConfig{
			table: basePath, entity: params.Entity, style: styleQuery,
			primaryKeys: []string{"id"}, incrementalKey: "dateLastModified", strategy: config.StrategyMerge,
		}
	}

	if cfg.style == styleSearch || cfg.style == styleQuery {
		if err := validateAllowlist(params.Fields); err != nil {
			return nil, fmt.Errorf("bullhorn table %q: %w", basePath, err)
		}
	}

	tsFields := slices.Concat(timestampFieldNames, params.TimestampFields)

	return &source.DynamicSourceTable{
		TableName:           basePath,
		TablePrimaryKeys:    cfg.primaryKeys,
		TableIncrementalKey: cfg.incrementalKey,
		TableStrategy:       cfg.strategy,
		KnownSchema:         false,
		SchemaFn: func(ctx context.Context) (*schema.TableSchema, error) {
			return nil, fmt.Errorf("bullhorn source does not have a predefined schema; schema inference is required")
		},
		ReadFn: func(ctx context.Context, opts source.ReadOptions) (<-chan source.RecordBatchResult, error) {
			return s.read(ctx, cfg, params, tsFields, opts)
		},
	}, nil
}

// isValidTable reports whether name is one of the built-in tables (brief
// §6.4). Ad hoc query-based entities (an unlisted table name with an explicit
// entity= parameter) are validated separately in GetTable.
func isValidTable(name string) bool {
	_, ok := builtinEntities[name]
	return ok
}

// validateAllowlist rejects a missing or wildcard field selection. Bullhorn
// API Fair Use Policy requires data minimisation; wildcard selection is
// prohibited by build brief §2.2.4/§6.4.3.
func validateAllowlist(fields []string) error {
	if len(fields) == 0 {
		return fmt.Errorf("requires an explicit fields allowlist; wildcard field selection is not permitted")
	}
	if slices.Contains(fields, "*") {
		return fmt.Errorf("wildcard field selection ('*') is not permitted; list the fields explicitly")
	}
	return nil
}

func (s *BullhornSource) read(ctx context.Context, cfg entityConfig, params tableParams, tsFields []string, opts source.ReadOptions) (<-chan source.RecordBatchResult, error) {
	results := make(chan source.RecordBatchResult, 8)

	go func() {
		defer close(results)

		var err error
		switch cfg.style {
		case styleSearch:
			err = s.readSearch(ctx, cfg, params.Fields, tsFields, opts, results)
		case styleQuery:
			err = s.readQuery(ctx, cfg, params.Fields, tsFields, opts, results)
		case styleMeta:
			err = s.readMeta(ctx, params.Entities, opts, results)
		case styleSubscription:
			err = s.readSubscription(ctx, params, opts, results)
		default:
			err = fmt.Errorf("bullhorn: unsupported endpoint style %q for table %q", cfg.style, cfg.table)
		}

		if err != nil {
			results <- source.RecordBatchResult{Err: err}
		}
	}()

	return results, nil
}

// buildLuceneRange constructs a dateLastModified range clause for the search
// (Lucene) endpoint. The exact bound-inclusivity is a §6.2.1 runbook item;
// this uses an inclusive range, isolated here so a confirmed syntax change is
// a one-function patch.
func buildLuceneRange(field string, start, end *time.Time) string {
	if start == nil && end == nil {
		return ""
	}
	lo := "*"
	if start != nil {
		lo = strconv.FormatInt(start.UnixMilli(), 10)
	}
	hi := "*"
	if end != nil {
		hi = strconv.FormatInt(end.UnixMilli(), 10)
	}
	return fmt.Sprintf("%s:[%s TO %s]", field, lo, hi)
}

// buildJPQLRange constructs a dateLastModified range clause for the query
// (JPQL) endpoint. Bound-inclusivity is a §6.2.1 runbook item.
func buildJPQLRange(field string, start, end *time.Time) string {
	var clauses []string
	if start != nil {
		clauses = append(clauses, fmt.Sprintf("%s >= %d", field, start.UnixMilli()))
	}
	if end != nil {
		clauses = append(clauses, fmt.Sprintf("%s < %d", field, end.UnixMilli()))
	}
	return strings.Join(clauses, " AND ")
}

func (s *BullhornSource) readSearch(ctx context.Context, cfg entityConfig, fields, tsFields []string, opts source.ReadOptions, results chan<- source.RecordBatchResult) error {
	config.Debug("[BULLHORN] reading %s via search", cfg.table)
	incField := cfg.incrementalKey
	query := "*:*"
	if incField != "" {
		if r := buildLuceneRange(incField, opts.IntervalStart, opts.IntervalEnd); r != "" {
			query = r
		}
	}
	return s.paginateAndSend(ctx, cfg.table, fields, tsFields, opts, results, func(count, start int) ([]map[string]any, error) {
		return s.fetchSearchPage(ctx, cfg.entity, query, fields, count, start)
	})
}

func (s *BullhornSource) readQuery(ctx context.Context, cfg entityConfig, fields, tsFields []string, opts source.ReadOptions, results chan<- source.RecordBatchResult) error {
	config.Debug("[BULLHORN] reading %s via query", cfg.table)
	incField := cfg.incrementalKey
	where := "id IS NOT NULL"
	if incField != "" {
		if r := buildJPQLRange(incField, opts.IntervalStart, opts.IntervalEnd); r != "" {
			where = r
		}
	}
	return s.paginateAndSend(ctx, cfg.table, fields, tsFields, opts, results, func(count, start int) ([]map[string]any, error) {
		return s.fetchQueryPage(ctx, cfg.entity, where, fields, count, start)
	})
}

// paginateAndSend pages through fetchPage (count/start pagination, per
// §6.1.8.1), applies the allowlist and timestamp conversion, and streams one
// Arrow batch per byte-bounded flush via opts.MaxBatchBytes.
func (s *BullhornSource) paginateAndSend(ctx context.Context, table string, fields, tsFields []string, opts source.ReadOptions, results chan<- source.RecordBatchResult, fetchPage func(count, start int) ([]map[string]any, error)) error {
	pageSize := defaultMaxPageSize
	start := 0
	total := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		raw, err := fetchPage(pageSize, start)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			break
		}

		var items []map[string]any
		var accBytes int64
		flush := func() error {
			if len(items) == 0 {
				return nil
			}
			record, err := arrowconv.ItemsToArrowRecordWithSchema(items, nil, opts.ExcludeColumns)
			if err != nil {
				return fmt.Errorf("bullhorn %s: failed to build arrow record: %w", table, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case results <- source.RecordBatchResult{Batch: record}:
			}
			items = nil
			accBytes = 0
			return nil
		}

		for _, rawItem := range raw {
			row := applyAllowlist(rawItem, fields)
			convertTimestampFields(row, tsFields)

			if opts.MaxBatchBytes > 0 {
				rowBytes := arrowconv.RowBytes(row)
				if len(items) > 0 && accBytes+rowBytes > opts.MaxBatchBytes {
					if err := flush(); err != nil {
						return err
					}
				}
				accBytes += rowBytes
			}
			items = append(items, row)
		}

		if err := flush(); err != nil {
			return err
		}

		total += len(raw)
		if len(raw) < pageSize {
			break
		}
		start += pageSize
	}

	config.Debug("[BULLHORN] finished reading %s: %d records", table, total)
	return nil
}

// applyAllowlist returns a copy of raw containing only the allowlisted
// fields, defensively enforcing data minimisation even if Bullhorn returns
// extra fields for a given `fields` request.
func applyAllowlist(raw map[string]any, fields []string) map[string]any {
	row := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := raw[f]; ok {
			row[f] = v
		}
	}
	return row
}

// convertTimestampFields converts each listed field, when present and
// numeric, from a Bullhorn epoch-millisecond value to a time.Time so the
// Arrow layer stores it at the microsecond convention (see
// reference/ingestr-upstream/CLAUDE.md).
func convertTimestampFields(row map[string]any, tsFields []string) {
	for _, f := range tsFields {
		v, ok := row[f]
		if !ok || v == nil {
			continue
		}
		ms, err := toInt64(v)
		if err != nil {
			continue
		}
		row[f] = time.UnixMilli(ms).UTC()
	}
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	case string:
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, fmt.Errorf("value %v is not a number", v)
	}
}

// jsonUseNumber decodes JSON preserving large integer precision (e.g.
// Bullhorn entity IDs) as json.Number rather than float64.
func jsonUseNumber(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// fetchSearchPage retrieves one page from the Lucene search endpoint,
// switching to the POST form once the query exceeds the documented GET
// length limit (brief §6.1.7.3).
func (s *BullhornSource) fetchSearchPage(ctx context.Context, entity, query string, fields []string, count, start int) ([]map[string]any, error) {
	endpoint := "/search/" + entity
	fieldsParam := strings.Join(fields, ",")

	resp, err := s.doRequest(ctx, entity, endpoint, func() (*httpclient.Response, error) {
		req := s.client.R(ctx).
			SetQueryParam("fields", fieldsParam).
			SetQueryParam("count", strconv.Itoa(count)).
			SetQueryParam("start", strconv.Itoa(start))
		if len(query) > searchQueryLengthLimit {
			return req.SetBody(map[string]string{"query": query}).Post(endpoint)
		}
		return req.SetQueryParam("query", query).Get(endpoint)
	})
	if err != nil {
		return nil, fmt.Errorf("bullhorn search %s: %w", entity, err)
	}
	return decodeDataEnvelope(resp.Body())
}

// fetchQueryPage retrieves one page from the JPQL query endpoint, switching
// to the POST form once the filter exceeds the documented GET length limit.
func (s *BullhornSource) fetchQueryPage(ctx context.Context, entity, where string, fields []string, count, start int) ([]map[string]any, error) {
	endpoint := "/query/" + entity
	fieldsParam := strings.Join(fields, ",")

	resp, err := s.doRequest(ctx, entity, endpoint, func() (*httpclient.Response, error) {
		req := s.client.R(ctx).
			SetQueryParam("fields", fieldsParam).
			SetQueryParam("count", strconv.Itoa(count)).
			SetQueryParam("start", strconv.Itoa(start))
		if len(where) > searchQueryLengthLimit {
			return req.SetBody(map[string]string{"where": where}).Post(endpoint)
		}
		return req.SetQueryParam("where", where).Get(endpoint)
	})
	if err != nil {
		return nil, fmt.Errorf("bullhorn query %s: %w", entity, err)
	}
	return decodeDataEnvelope(resp.Body())
}

// decodeDataEnvelope unwraps Bullhorn's {"data": [...]}, response envelope
// used by both search and query.
func decodeDataEnvelope(body []byte) ([]map[string]any, error) {
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := jsonUseNumber(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return envelope.Data, nil
}

// metaField mirrors one entry in the Bullhorn /meta/{entity} field list.
type metaField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	DataType string `json:"dataType"`
}

// readMeta fetches field definitions (never entity data) for each requested
// entity, satisfying brief §6.4.3: entity_metadata records field names and
// labels only. fields=* here is safe because /meta never returns candidate
// or contact data, only schema definitions.
func (s *BullhornSource) readMeta(ctx context.Context, entities []string, opts source.ReadOptions, results chan<- source.RecordBatchResult) error {
	if len(entities) == 0 {
		return fmt.Errorf("entity_metadata requires an explicit entities= parameter listing the entities to describe")
	}

	var items []map[string]any
	for _, entity := range entities {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		endpoint := "/meta/" + entity
		resp, err := s.doRequest(ctx, entity, endpoint, func() (*httpclient.Response, error) {
			return s.client.R(ctx).SetQueryParam("fields", "*").Get(endpoint)
		})
		if err != nil {
			return fmt.Errorf("bullhorn meta %s: %w", entity, err)
		}

		var body struct {
			Entity string      `json:"entity"`
			Fields []metaField `json:"fields"`
		}
		if err := jsonUseNumber(resp.Body(), &body); err != nil {
			return fmt.Errorf("bullhorn meta %s: failed to parse response: %w", entity, err)
		}

		for _, f := range body.Fields {
			items = append(items, map[string]any{
				"entity":     entity,
				"field_name": f.Name,
				"label":      f.Label,
				"type":       f.Type,
				"data_type":  f.DataType,
			})
		}
	}

	if len(items) == 0 {
		return nil
	}
	record, err := arrowconv.ItemsToArrowRecordWithSchema(items, nil, opts.ExcludeColumns)
	if err != nil {
		return fmt.Errorf("bullhorn entity_metadata: failed to build arrow record: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case results <- source.RecordBatchResult{Batch: record}:
	}
	return nil
}

type subscriptionEvent struct {
	EventID         string `json:"eventId"`
	EventTimestamp  int64  `json:"eventTimestamp"`
	EntityName      string `json:"entityName"`
	EntityID        any    `json:"entityId"`
	EntityEventType string `json:"entityEventType"`
}

// readSubscription polls a Bullhorn event subscription for DELETED events
// and writes them to deleted_records (brief §6.1.11, §6.5.2). The
// subscription is created idempotently on first use. requestId is persisted
// locally so a standalone run of this source (outside the runner) resumes
// correctly; the runner may also track and advance it (brief §9.3.4).
func (s *BullhornSource) readSubscription(ctx context.Context, params tableParams, opts source.ReadOptions, results chan<- source.RecordBatchResult) error {
	if len(params.Entities) == 0 {
		return fmt.Errorf("deleted_records requires an explicit entities= parameter listing the entities to watch")
	}
	subscriptionID := params.SubscriptionID
	if subscriptionID == "" {
		subscriptionID = "ingestr"
	}
	statePath := params.StatePath
	if statePath == "" {
		statePath = s.creds.tokenOutput + ".subscription_state.json"
	}

	if err := s.ensureSubscription(ctx, subscriptionID, params.Entities); err != nil {
		return fmt.Errorf("bullhorn subscription %s: %w", subscriptionID, err)
	}

	lastRequestID, err := loadSubscriptionState(statePath)
	if err != nil {
		return fmt.Errorf("bullhorn subscription %s: failed to load state: %w", subscriptionID, err)
	}

	endpoint := "/event/subscription/" + subscriptionID
	const maxEvents = 100

	resp, err := s.doRequest(ctx, "subscription", endpoint, func() (*httpclient.Response, error) {
		req := s.client.R(ctx).SetQueryParam("maxEvents", strconv.Itoa(maxEvents))
		if lastRequestID > 0 {
			req = req.SetQueryParam("requestId", strconv.FormatInt(lastRequestID, 10))
		}
		return req.Get(endpoint)
	})
	if err != nil {
		return fmt.Errorf("bullhorn subscription %s: %w", subscriptionID, err)
	}

	var body struct {
		RequestID int64               `json:"requestId"`
		Events    []subscriptionEvent `json:"events"`
	}
	if err := jsonUseNumber(resp.Body(), &body); err != nil {
		return fmt.Errorf("bullhorn subscription %s: failed to parse response: %w", subscriptionID, err)
	}

	var items []map[string]any
	for _, ev := range body.Events {
		if ev.EntityEventType != "DELETED" {
			continue
		}
		items = append(items, map[string]any{
			"entity":          ev.EntityName,
			"entity_id":       ev.EntityID,
			"event_id":        ev.EventID,
			"event_type":      "hard_delete",
			"event_timestamp": time.UnixMilli(ev.EventTimestamp).UTC(),
		})
	}

	if len(items) > 0 {
		record, err := arrowconv.ItemsToArrowRecordWithSchema(items, nil, opts.ExcludeColumns)
		if err != nil {
			return fmt.Errorf("bullhorn deleted_records: failed to build arrow record: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case results <- source.RecordBatchResult{Batch: record}:
		}
	}

	// Advance the persisted cursor only after the batch has been queued for
	// the destination, mirroring the runner's own requestId-after-write rule
	// (brief §9.3.4).
	if body.RequestID > 0 {
		if err := saveSubscriptionState(statePath, body.RequestID); err != nil {
			return fmt.Errorf("bullhorn subscription %s: failed to persist state: %w", subscriptionID, err)
		}
	}

	return nil
}

type bullhornCredentials struct {
	clientID     string
	clientSecret string
	refreshToken string
	username     string
	password     string
	dataCenter   string
	rateLimit    float64
	tokenOutput  string
}

func parseURI(uri string) (bullhornCredentials, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return bullhornCredentials{}, fmt.Errorf("invalid bullhorn URI: %w", err)
	}
	if parsed.Scheme != "bullhorn" {
		return bullhornCredentials{}, fmt.Errorf("invalid bullhorn URI: must start with bullhorn://")
	}

	q := parsed.Query()

	clientID := q.Get("client_id")
	if clientID == "" {
		return bullhornCredentials{}, fmt.Errorf("client_id is required in bullhorn URI")
	}
	clientSecret := q.Get("client_secret")
	if clientSecret == "" {
		return bullhornCredentials{}, fmt.Errorf("client_secret is required in bullhorn URI")
	}

	refreshToken := q.Get("refresh_token")
	username := q.Get("username")
	password := q.Get("password")
	if refreshToken == "" && (username == "" || password == "") {
		return bullhornCredentials{}, fmt.Errorf("bullhorn requires either refresh_token, or username and password, in the URI")
	}

	dataCenter := q.Get("data_center")
	if dataCenter == "" {
		return bullhornCredentials{}, fmt.Errorf("data_center is required in bullhorn URI")
	}

	rateLimitStr := q.Get("rate_limit")
	if rateLimitStr == "" {
		return bullhornCredentials{}, fmt.Errorf("rate_limit is required in bullhorn URI; there is no vendor-derived default (see README)")
	}
	rateLimit, err := strconv.ParseFloat(rateLimitStr, 64)
	if err != nil || rateLimit <= 0 {
		return bullhornCredentials{}, fmt.Errorf("rate_limit must be a positive number, got %q", rateLimitStr)
	}

	tokenOutput := q.Get("token_output")
	if tokenOutput == "" {
		return bullhornCredentials{}, fmt.Errorf("token_output is required in bullhorn URI")
	}

	return bullhornCredentials{
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
		username:     username,
		password:     password,
		dataCenter:   dataCenter,
		rateLimit:    rateLimit,
		tokenOutput:  tokenOutput,
	}, nil
}
