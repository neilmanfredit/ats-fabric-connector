package main

import (
	"testing"
	"time"
)

func TestDiffCounts_Match(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	r := diffCounts("candidate", now, 100, 100)
	if !r.Match {
		t.Fatal("expected match when counts are equal")
	}
	if r.CheckType != "count" {
		t.Fatalf("expected check_type=count, got %q", r.CheckType)
	}
}

func TestDiffCounts_Mismatch(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	r := diffCounts("candidate", now, 100, 97)
	if r.Match {
		t.Fatal("expected mismatch when counts differ")
	}
	if r.BronzeCount != 100 || r.BullhornCount != 97 {
		t.Fatalf("unexpected counts: %+v", r)
	}
}

func TestDiffKeys_NoMissing(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	bronze := []string{"1", "2", "3"}
	bullhorn := []string{"1", "2", "3"}
	r := diffKeys("candidate", now, bronze, bullhorn)
	if !r.Match {
		t.Fatal("expected match when key sets are identical")
	}
	if len(r.MissingKeys) != 0 {
		t.Fatalf("expected no missing keys, got %v", r.MissingKeys)
	}
}

func TestDiffKeys_FindsMissing(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	bronze := []string{"1", "2"}
	bullhorn := []string{"1", "2", "3", "4"}
	r := diffKeys("candidate", now, bronze, bullhorn)
	if r.Match {
		t.Fatal("expected mismatch when Bullhorn has extra keys")
	}
	if len(r.MissingKeys) != 2 {
		t.Fatalf("expected 2 missing keys, got %v", r.MissingKeys)
	}
	seen := map[string]bool{}
	for _, k := range r.MissingKeys {
		seen[k] = true
	}
	if !seen["3"] || !seen["4"] {
		t.Fatalf("expected missing keys 3 and 4, got %v", r.MissingKeys)
	}
}

func TestDiffKeys_IgnoresBronzeOnlyKeys(t *testing.T) {
	// Bronze holding a key Bullhorn no longer reports is not this check's
	// job (that's a hard/soft delete, handled elsewhere) — it must not show
	// up as "missing".
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	bronze := []string{"1", "2", "3"}
	bullhorn := []string{"1", "2"}
	r := diffKeys("candidate", now, bronze, bullhorn)
	if len(r.MissingKeys) != 0 {
		t.Fatalf("expected no missing keys (bronze-only keys are not reconciliation gaps), got %v", r.MissingKeys)
	}
}

func TestReconciliationDeletedRecords_Shape(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	rows := reconciliationDeletedRecords("Candidate", now, []string{"10", "20"})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for i, id := range []string{"10", "20"} {
		if rows[i].Entity != "Candidate" {
			t.Fatalf("row %d: expected entity Candidate, got %q", i, rows[i].Entity)
		}
		if rows[i].EntityID != id {
			t.Fatalf("row %d: expected entity_id %q, got %q", i, id, rows[i].EntityID)
		}
		if rows[i].EventType != "reconciliation" {
			t.Fatalf("row %d: expected event_type reconciliation, got %q", i, rows[i].EventType)
		}
		if !rows[i].EventTimestamp.Equal(now) {
			t.Fatalf("row %d: expected event_timestamp %v, got %v", i, now, rows[i].EventTimestamp)
		}
	}
}

func TestReconciliationDeletedRecords_EmptyWhenNoMissing(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	rows := reconciliationDeletedRecords("Candidate", now, nil)
	if len(rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(rows))
	}
}

func TestBuildOnelakeURI(t *testing.T) {
	got := buildOnelakeURI("ws", "lh")
	want := "onelake://ws/lh?use_azure_default_credential=true"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := buildOnelakeURI("", "lh"); got != "" {
		t.Fatalf("expected empty string when workspace is missing, got %q", got)
	}
}
