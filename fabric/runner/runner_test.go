package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.yaml")
	yaml := `
entities:
  - table: candidate
    entity: Candidate
    endpoint: search
    primary_key: [id]
    incremental_key: dateLastModified
    strategy: merge
    schedule_group: frequent
    critical: true
    allowlist: [id, dateLastModified]
runner:
  watermark_overlap: 5m
  budget_threshold_fraction: 0.8
  monthly_call_limit: 100000
  secret_store: file
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Entities) != 1 || cfg.Entities[0].Table != "candidate" {
		t.Fatalf("unexpected entities: %+v", cfg.Entities)
	}
	if cfg.Runner.WatermarkOverlap.AsDuration() != 5*time.Minute {
		t.Fatalf("expected 5m overlap, got %v", cfg.Runner.WatermarkOverlap.AsDuration())
	}
}

func TestLoadConfig_RejectsMissingAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.yaml")
	yaml := `
entities:
  - table: candidate
    entity: Candidate
    endpoint: search
    primary_key: [id]
    strategy: merge
    schedule_group: frequent
    critical: true
runner:
  watermark_overlap: 5m
  budget_threshold_fraction: 0.8
  monthly_call_limit: 100000
  secret_store: file
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for missing allowlist, got nil")
	}
}

func TestLoadConfig_RejectsWildcardAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.yaml")
	yaml := `
entities:
  - table: candidate
    entity: Candidate
    endpoint: search
    primary_key: [id]
    strategy: merge
    schedule_group: frequent
    critical: true
    allowlist: ["*"]
runner:
  watermark_overlap: 5m
  budget_threshold_fraction: 0.8
  monthly_call_limit: 100000
  secret_store: file
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for wildcard allowlist, got nil")
	}
}

func TestLoadConfig_RejectsDuplicateTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.yaml")
	yaml := `
entities:
  - table: candidate
    endpoint: search
    strategy: merge
    allowlist: [id]
  - table: candidate
    endpoint: search
    strategy: merge
    allowlist: [id]
runner:
  watermark_overlap: 5m
  budget_threshold_fraction: 0.8
  monthly_call_limit: 100000
  secret_store: file
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for duplicate table, got nil")
	}
}

func TestComputeIntervalStart_UsesWatermarkMinusOverlap(t *testing.T) {
	wm := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	overlap := 5 * time.Minute
	got := computeIntervalStart(wm, nil, overlap)
	want := wm.Add(-overlap)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeIntervalStart_FallsBackToSeedWatermark(t *testing.T) {
	seed := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	overlap := 5 * time.Minute
	got := computeIntervalStart(time.Time{}, &seed, overlap)
	want := seed.Add(-overlap)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeIntervalStart_ZeroWhenNoWatermarkOrSeed(t *testing.T) {
	got := computeIntervalStart(time.Time{}, nil, 5*time.Minute)
	if !got.IsZero() {
		t.Fatalf("expected zero time, got %v", got)
	}
}

func TestShouldRunEntity_CriticalAlwaysRuns(t *testing.T) {
	e := EntityConfig{Critical: true}
	if !shouldRunEntity(e, true) {
		t.Fatal("critical entity must run even when over budget")
	}
}

func TestShouldRunEntity_NonCriticalSkippedOverBudget(t *testing.T) {
	e := EntityConfig{Critical: false}
	if shouldRunEntity(e, true) {
		t.Fatal("non-critical entity must be skipped when over budget")
	}
	if !shouldRunEntity(e, false) {
		t.Fatal("non-critical entity must run when under budget")
	}
}

func TestProjectedMonthEndCalls_MidMonth(t *testing.T) {
	// Exactly half of a 30-day month elapsed, 1000 calls so far -> ~2000 projected.
	now := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC) // April has 30 days
	got := projectedMonthEndCalls(now, 1000)
	if got < 1900 || got > 2100 {
		t.Fatalf("expected ~2000, got %v", got)
	}
}

func TestProjectedMonthEndCalls_FloorsElapsedTime(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 0, 1, 0, time.UTC) // one second into the month
	got := projectedMonthEndCalls(now, 10)
	// With elapsed floored at one hour, projection should be finite and large
	// but not astronomically so (i.e. not dividing by ~1 second).
	if got <= 0 || got > 1_000_000 {
		t.Fatalf("expected a bounded projection, got %v", got)
	}
}

func TestBudgetExceeded(t *testing.T) {
	if !budgetExceeded(90_000, 100_000, 0.8) {
		t.Fatal("expected 90000 to exceed 80% of 100000")
	}
	if budgetExceeded(70_000, 100_000, 0.8) {
		t.Fatal("expected 70000 to be under 80% of 100000")
	}
}

func TestBullhornSourceTable_SearchEncodesAllowlist(t *testing.T) {
	e := EntityConfig{Table: "candidate", Endpoint: "search", Allowlist: []string{"id", "dateLastModified"}}
	got := bullhornSourceTable(e)
	want := "candidate?fields=id,dateLastModified"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBullhornSourceTable_MetaEncodesEntities(t *testing.T) {
	e := EntityConfig{Table: "entity_metadata", Endpoint: "meta", MetaEntities: []string{"Candidate", "JobOrder"}}
	got := bullhornSourceTable(e)
	want := "entity_metadata?entities=Candidate,JobOrder"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseCallCount(t *testing.T) {
	if n, ok := parseCallCount("42\n"); !ok || n != 42 {
		t.Fatalf("got (%d, %v), want (42, true)", n, ok)
	}
	if _, ok := parseCallCount(""); ok {
		t.Fatal("expected ok=false for empty input")
	}
	if _, ok := parseCallCount("not-a-number"); ok {
		t.Fatal("expected ok=false for non-numeric input")
	}
}

func TestAcquireLock_FailsWhenAlreadyHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if _, err := AcquireLock(path); err == nil {
		t.Fatal("expected second AcquireLock to fail while lock is held")
	}
}

func TestAcquireLock_ReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	lock2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestState_AddCallsAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	st := newState()
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	st.AddCalls(now, 5)
	st.AddCalls(now, 3)
	if got := st.CallsThisMonth(now); got != 8 {
		t.Fatalf("got %d calls, want 8", got)
	}

	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := reloaded.CallsThisMonth(now); got != 8 {
		t.Fatalf("reloaded got %d calls, want 8", got)
	}
}

func TestFileSecretStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	store := NewFileSecretStore(path)

	if err := store.SetSecret(context.Background(), "bullhorn_refresh_token", "abc123"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	got, err := store.GetSecret(context.Background(), "bullhorn_refresh_token")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got != "abc123" {
		t.Fatalf("got %q, want %q", got, "abc123")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected owner-only permissions, got %v", info.Mode().Perm())
	}
}
