package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is persisted at Files/control/bullhorn_state.json in the Lakehouse
// (build brief section 9.2). A local path is used when run outside Fabric.
type State struct {
	// Watermarks holds the last successful incremental-key value per table.
	Watermarks map[string]time.Time `json:"watermarks"`
	// SubscriptionRequestID is the last Bullhorn event-subscription requestId
	// read for deleted_records (build brief section 6.1.11.2).
	SubscriptionRequestID string `json:"subscription_request_id"`
	// MonthToDateCalls maps a "YYYY-MM" bucket to the call count recorded so
	// far that month (build brief section 6.6.1).
	MonthToDateCalls map[string]int64 `json:"month_to_date_calls"`
	LastRun          time.Time        `json:"last_run"`
}

func newState() *State {
	return &State{
		Watermarks:       map[string]time.Time{},
		MonthToDateCalls: map[string]int64{},
	}
}

func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state %s: %w", path, err)
	}
	st := newState()
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("parsing state %s: %w", path, err)
	}
	if st.Watermarks == nil {
		st.Watermarks = map[string]time.Time{}
	}
	if st.MonthToDateCalls == nil {
		st.MonthToDateCalls = map[string]int64{}
	}
	return st, nil
}

// Save writes state atomically (temp file + rename in the same directory).
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating state directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".bullhorn_state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp state file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp state file into place: %w", err)
	}
	return nil
}

func monthBucket(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// AddCalls records call usage against the current month's bucket. 429
// responses must not be passed here (build brief section 6.1.13).
func (s *State) AddCalls(now time.Time, n int64) {
	if s.MonthToDateCalls == nil {
		s.MonthToDateCalls = map[string]int64{}
	}
	s.MonthToDateCalls[monthBucket(now)] += n
}

func (s *State) CallsThisMonth(now time.Time) int64 {
	return s.MonthToDateCalls[monthBucket(now)]
}
