package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// computeIntervalStart returns the start of the incremental window for one
// entity: the last watermark minus the configured overlap, or the seed
// watermark for a table that has never run before (build brief section 8.4,
// 9.3.2). If neither is available, the caller is expected to run a wide
// first window and warn — this function returns the zero time in that case.
func computeIntervalStart(watermark time.Time, seedWatermark *time.Time, overlap time.Duration) time.Time {
	if !watermark.IsZero() {
		return watermark.Add(-overlap)
	}
	if seedWatermark != nil {
		return seedWatermark.Add(-overlap)
	}
	return time.Time{}
}

// shouldRunEntity applies the budget skip rule: critical entities always
// run; non-critical entities are skipped once projected month-end usage
// exceeds the configured threshold (build brief section 6.6.2).
func shouldRunEntity(e EntityConfig, budgetExceeded bool) bool {
	if e.Critical {
		return true
	}
	return !budgetExceeded
}

// bullhornSourceTable builds the --source-table value, carrying the field
// allowlist (or entity list, for meta/subscription tables) as query
// parameters, per the bullhorn source's tablespec-based table parsing.
func bullhornSourceTable(e EntityConfig) string {
	switch e.Endpoint {
	case "search", "query":
		return fmt.Sprintf("%s?fields=%s", e.Table, strings.Join(e.Allowlist, ","))
	case "meta":
		return fmt.Sprintf("%s?entities=%s", e.Table, strings.Join(e.MetaEntities, ","))
	case "subscription":
		return fmt.Sprintf("%s?entities=%s", e.Table, strings.Join(e.SubscribedEntities, ","))
	default:
		return e.Table
	}
}

// buildIngestrArgs assembles the ingestr CLI arguments for one entity run.
// intervalStart may be the zero time, meaning "unbounded start" (a full
// fetch); ingestr omits --interval-start in that case. sourceTable is built
// by the caller via bullhornSourceTable, with any extra query params (e.g.
// state_path) appended.
func buildIngestrArgs(e EntityConfig, sourceURI, sourceTable, destURI string, intervalStart, intervalEnd time.Time) []string {
	args := []string{
		"ingest",
		"--source-uri=" + sourceURI,
		"--source-table=" + sourceTable,
		"--dest-uri=" + destURI,
		"--dest-table=bullhorn." + e.Table,
		"--incremental-strategy=" + e.Strategy,
		"--yes",
	}
	if len(e.PrimaryKey) > 0 {
		args = append(args, "--primary-key="+strings.Join(e.PrimaryKey, ","))
	}
	if e.IncrementalKey != "" {
		args = append(args, "--incremental-key="+e.IncrementalKey)
		if !intervalStart.IsZero() {
			args = append(args, "--interval-start="+intervalStart.UTC().Format(time.RFC3339))
		}
		args = append(args, "--interval-end="+intervalEnd.UTC().Format(time.RFC3339))
	}
	return args
}

// parseCallCount parses the single integer written by the source to the
// call-count file (see runCallCountFile in main.go). Returns 0, false if the
// file was absent or unparsable, which the caller treats as "unknown" rather
// than a hard failure — call accounting degrades gracefully until the source
// side of this integration point is confirmed against the live source.
func parseCallCount(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
