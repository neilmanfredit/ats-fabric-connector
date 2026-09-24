// Command afc-reconcile compares Bronze against Bullhorn on two independent
// schedules (build brief section 10): a cheap daily count comparison using
// totalOnly, and a heavier ID-only key comparison that also detects missed
// deletes.
//
// Results are written through ingestr's own onelake destination (jsonl
// source -> onelake dest), rather than a hand-rolled Delta writer, keeping
// every OneLake write in the project on the one supported path.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// reconciledEntity is one entity table subject to reconciliation. Metadata
// and deleted_records are not reconciled against themselves.
type reconciledEntity struct {
	table     string
	entity    string
	dateField string
	keyColumn string
}

func defaultEntities() []reconciledEntity {
	return []reconciledEntity{
		{table: "candidate", entity: "Candidate", dateField: "dateLastModified", keyColumn: "id"},
		{table: "client_corporation", entity: "ClientCorporation", dateField: "dateLastModified", keyColumn: "id"},
		{table: "client_contact", entity: "ClientContact", dateField: "dateLastModified", keyColumn: "id"},
		{table: "job_order", entity: "JobOrder", dateField: "dateLastModified", keyColumn: "id"},
		{table: "placement", entity: "Placement", dateField: "dateLastModified", keyColumn: "id"},
		{table: "job_submission", entity: "JobSubmission", dateField: "dateLastModified", keyColumn: "id"},
		{table: "corporate_user", entity: "CorporateUser", dateField: "dateLastModified", keyColumn: "id"},
	}
}

// configEntity is the subset of fabric/runner's EntityConfig YAML shape that
// reconciliation needs. Duplicated rather than imported: both are separate
// `main` packages (see fabric/REPORT.md deviation on the standalone Bullhorn
// client for the same reason).
type configEntity struct {
	Table          string   `yaml:"table"`
	Entity         string   `yaml:"entity"`
	Endpoint       string   `yaml:"endpoint"`
	PrimaryKey     []string `yaml:"primary_key"`
	IncrementalKey string   `yaml:"incremental_key"`
}

type configFile struct {
	Entities []configEntity `yaml:"entities"`
}

// loadReconciledEntities reads entities.yaml (the same file the runner
// consumes) and keeps only the search/query entities with a primary key —
// entity_metadata and deleted_records aren't reconciled against Bullhorn
// counts/keys the same way. An empty path, or a config with no reconcilable
// entities, falls back to defaultEntities().
func loadReconciledEntities(path string) ([]reconciledEntity, error) {
	if path == "" {
		return defaultEntities(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading entities config %s: %w", path, err)
	}
	var cfg configFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing entities config %s: %w", path, err)
	}
	var out []reconciledEntity
	for _, e := range cfg.Entities {
		if e.Endpoint != "search" && e.Endpoint != "query" {
			continue
		}
		if len(e.PrimaryKey) == 0 {
			continue
		}
		out = append(out, reconciledEntity{
			table:     e.Table,
			entity:    e.Entity,
			dateField: e.IncrementalKey,
			keyColumn: e.PrimaryKey[0],
		})
	}
	if len(out) == 0 {
		return defaultEntities(), nil
	}
	return out, nil
}

// normalizeMode accepts the singular or plural spelling of each mode so the
// README's "counts"/"keys" and the flag's original "count"/"keys" both work.
func normalizeMode(mode string) (string, bool) {
	switch mode {
	case "count", "counts":
		return "count", true
	case "key", "keys":
		return "keys", true
	default:
		return "", false
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	os.Exit(run())
}

func run() int {
	modeFlag := flag.String("mode", "count", "reconciliation mode: count(s) or key(s)")
	configPath := flag.String("config", "", "path to entities.yaml (defaults to the built-in entity list)")
	flag.Parse()

	logger := log.New(os.Stdout, "", 0)

	mode, ok := normalizeMode(*modeFlag)
	if !ok {
		logger.Printf(`{"level":"error","msg":"unknown mode %q, expected count(s) or key(s)"}`, *modeFlag)
		return 1
	}

	sqlEndpoint := os.Getenv("FABRIC_SQL_ENDPOINT")
	onelakeURI := buildOnelakeURI(os.Getenv("ONELAKE_WORKSPACE"), os.Getenv("ONELAKE_LAKEHOUSE"))
	if sqlEndpoint == "" || onelakeURI == "" {
		logger.Print(`{"level":"error","msg":"FABRIC_SQL_ENDPOINT, ONELAKE_WORKSPACE and ONELAKE_LAKEHOUSE are required"}`)
		return 1
	}

	bronze, err := NewSQLBronzeReader(sqlEndpoint, envOr("AFC_BRONZE_SCHEMA", "bullhorn"))
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return 1
	}
	defer func() { _ = bronze.Close() }()

	ctx := context.Background()
	bullhorn, err := NewBullhornCounter(
		ctx,
		os.Getenv("BULLHORN_CLIENT_ID"),
		os.Getenv("BULLHORN_CLIENT_SECRET"),
		os.Getenv("BULLHORN_REFRESH_TOKEN"),
		os.Getenv("BULLHORN_USERNAME"),
		os.Getenv("BULLHORN_DATA_CENTER"),
	)
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return 1
	}

	entities, err := loadReconciledEntities(*configPath)
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return 1
	}
	now := time.Now().UTC()
	ingestrBin := envOr("AFC_INGESTR_BIN", "ingestr")

	var logRows []CountResult
	var deletedRows []DeletedRecordRow
	anyFailed := false

	switch mode {
	case "count":
		since := now.AddDate(0, 0, -1) // daily pass compares the last 24h of change volume
		for _, e := range entities {
			bronzeCount, err := bronze.CountRows(ctx, e.table)
			if err != nil {
				logger.Printf(`{"table":%q,"status":"error","error":%q}`, e.table, err.Error())
				anyFailed = true
				continue
			}
			bullhornCount, err := bullhorn.TotalCount(ctx, e.entity, e.dateField, since)
			if err != nil {
				logger.Printf(`{"table":%q,"status":"error","error":%q}`, e.table, err.Error())
				anyFailed = true
				continue
			}
			result := diffCounts(e.table, now, bronzeCount, bullhornCount)
			logRows = append(logRows, result)
			logger.Printf(`{"table":%q,"status":"ok","match":%v,"bronze_count":%d,"bullhorn_count":%d}`,
				e.table, result.Match, result.BronzeCount, result.BullhornCount)
		}

	case "keys":
		for _, e := range entities {
			bronzeKeys, err := bronze.ListKeys(ctx, e.table, e.keyColumn)
			if err != nil {
				logger.Printf(`{"table":%q,"status":"error","error":%q}`, e.table, err.Error())
				anyFailed = true
				continue
			}
			bullhornKeys, err := bullhorn.ListIDs(ctx, e.entity)
			if err != nil {
				logger.Printf(`{"table":%q,"status":"error","error":%q}`, e.table, err.Error())
				anyFailed = true
				continue
			}
			result := diffKeys(e.table, now, bronzeKeys, bullhornKeys)
			logRows = append(logRows, result.CountResult)
			rows := reconciliationDeletedRecords(e.entity, now, result.MissingKeys)
			deletedRows = append(deletedRows, rows...)
			logger.Printf(`{"table":%q,"status":"ok","match":%v,"missing_keys":%d}`,
				e.table, result.Match, len(result.MissingKeys))
		}

	default:
		// Unreachable: normalizeMode already rejected anything else above.
		logger.Printf(`{"level":"error","msg":"unknown mode %q, expected count(s) or key(s)"}`, mode)
		return 1
	}

	if len(logRows) > 0 {
		if err := writeViaIngestr(ingestrBin, "reconciliation_log", []string{"table", "check_type", "run_at"}, onelakeURI, logRows); err != nil {
			logger.Printf(`{"level":"error","msg":%q}`, err.Error())
			anyFailed = true
		}
	}
	if len(deletedRows) > 0 {
		if err := writeViaIngestr(ingestrBin, "deleted_records", []string{"entity", "entity_id", "event_id"}, onelakeURI, deletedRows); err != nil {
			logger.Printf(`{"level":"error","msg":%q}`, err.Error())
			anyFailed = true
		}
	}

	if anyFailed {
		return 1
	}
	return 0
}

func buildOnelakeURI(workspace, lakehouse string) string {
	if workspace == "" || lakehouse == "" {
		return ""
	}
	return fmt.Sprintf("onelake://%s/%s?use_azure_default_credential=true", workspace, lakehouse)
}

// writeViaIngestr writes rows as JSON Lines to a temp file, then loads them
// through ingestr's jsonl source and the onelake destination, merging on the
// given primary key.
func writeViaIngestr[T any](ingestrBin, destTable string, primaryKey []string, onelakeURI string, rows []T) error {
	tmp, err := os.CreateTemp("", destTable+"-*.jsonl")
	if err != nil {
		return fmt.Errorf("creating temp jsonl file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	enc := json.NewEncoder(tmp)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("encoding row for %s: %w", destTable, err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp jsonl file: %w", err)
	}

	args := []string{
		"ingest",
		"--source-uri=jsonl://" + tmp.Name(),
		"--source-table=data",
		"--dest-uri=" + onelakeURI,
		"--dest-table=bullhorn." + destTable,
		"--incremental-strategy=merge",
		"--primary-key=" + strings.Join(primaryKey, ","),
		"--yes",
	}
	cmd := exec.Command(ingestrBin, args...)
	cmd.Env = append(os.Environ(), "INGESTR_DISABLE_TELEMETRY=true", "DISABLE_TELEMETRY=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("writing %s via ingestr: %w: %s", destTable, err, string(out))
	}
	return nil
}
