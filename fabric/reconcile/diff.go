package main

import "time"

// CountResult is one row written to Tables/bullhorn/reconciliation_log for
// the daily count-comparison pass (build brief section 10.1, 10.3).
type CountResult struct {
	Table         string    `json:"table"`
	CheckType     string    `json:"check_type"` // "count" or "keys"
	RunAt         time.Time `json:"run_at"`
	BronzeCount   int64     `json:"bronze_count"`
	BullhornCount int64     `json:"bullhorn_count"`
	Match         bool      `json:"match"`
}

// KeyResult is one row written to Tables/bullhorn/reconciliation_log for the
// ID-only key-comparison pass, plus the missing keys destined for
// deleted_records.
type KeyResult struct {
	CountResult
	MissingKeys []string `json:"missing_keys"`
}

func diffCounts(table string, now time.Time, bronzeCount, bullhornCount int64) CountResult {
	return CountResult{
		Table:         table,
		CheckType:     "count",
		RunAt:         now,
		BronzeCount:   bronzeCount,
		BullhornCount: bullhornCount,
		Match:         bronzeCount == bullhornCount,
	}
}

// diffKeys returns the keys present in bullhornKeys but absent from
// bronzeKeys: Bullhorn records Bronze has never seen (a genuine gap) or has
// lost (build brief section 6.5.3, 10.2).
func diffKeys(table string, now time.Time, bronzeKeys, bullhornKeys []string) KeyResult {
	bronzeSet := make(map[string]struct{}, len(bronzeKeys))
	for _, k := range bronzeKeys {
		bronzeSet[k] = struct{}{}
	}

	var missing []string
	for _, k := range bullhornKeys {
		if _, ok := bronzeSet[k]; !ok {
			missing = append(missing, k)
		}
	}

	return KeyResult{
		CountResult: CountResult{
			Table:         table,
			CheckType:     "keys",
			RunAt:         now,
			BronzeCount:   int64(len(bronzeKeys)),
			BullhornCount: int64(len(bullhornKeys)),
			Match:         len(missing) == 0,
		},
		MissingKeys: missing,
	}
}

// DeletedRecordRow shapes one row for the deleted_records table, sourced
// from reconciliation (build brief section 6.5.3): a key Bullhorn reports
// but Bronze never received or has lost.
type DeletedRecordRow struct {
	Entity         string    `json:"entity"`
	EntityID       string    `json:"entity_id"`
	EventID        string    `json:"event_id"`
	EventType      string    `json:"event_type"`
	EventTimestamp time.Time `json:"event_timestamp"`
}

func reconciliationDeletedRecords(entity string, now time.Time, missingKeys []string) []DeletedRecordRow {
	rows := make([]DeletedRecordRow, 0, len(missingKeys))
	for _, id := range missingKeys {
		rows = append(rows, DeletedRecordRow{
			Entity:         entity,
			EntityID:       id,
			EventID:        "reconciliation-" + id,
			EventType:      "reconciliation",
			EventTimestamp: now,
		})
	}
	return rows
}
