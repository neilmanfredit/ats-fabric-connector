// Command afc-runner computes watermarks, enforces the Bullhorn API budget,
// and invokes ingestr once per configured entity (build brief section 9).
//
// State (watermarks, subscription requestId, month-to-date call count) is
// expected at Files/control/bullhorn_state.json inside the Fabric Lakehouse;
// AFC_STATE_PATH overrides this for local runs and tests.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	exitOK             = 0
	exitEntityFailed   = 1
	exitBudgetWarning  = 2
	defaultOverlap     = 5 * time.Minute
	fallbackConfigPath = "fabric/runner/entities.yaml"
	fallbackStatePath  = "fabric/state/bullhorn_state.json"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type runnerEnv struct {
	configPath   string
	statePath    string
	lockPath     string
	ingestrBin   string
	scheduleOnly string

	clientID    string
	username    string
	dataCenter  string
	rateLimit   string
	onelakeURI  string
	secretPath  string
	keyVaultURL string
	tokenTmpDir string
	callTmpDir  string
}

func loadRunnerEnv() runnerEnv {
	statePath := envOr("AFC_STATE_PATH", fallbackStatePath)
	return runnerEnv{
		configPath:   envOr("AFC_ENTITIES_CONFIG", fallbackConfigPath),
		statePath:    statePath,
		lockPath:     envOr("AFC_LOCK_PATH", statePath+".lock"),
		ingestrBin:   envOr("AFC_INGESTR_BIN", "ingestr"),
		scheduleOnly: os.Getenv("AFC_SCHEDULE_GROUP"),

		clientID:    os.Getenv("BULLHORN_CLIENT_ID"),
		username:    os.Getenv("BULLHORN_USERNAME"),
		dataCenter:  os.Getenv("BULLHORN_DATA_CENTER"),
		rateLimit:   os.Getenv("BULLHORN_RATE_LIMIT"),
		onelakeURI:  buildOnelakeURI(os.Getenv("ONELAKE_WORKSPACE"), os.Getenv("ONELAKE_LAKEHOUSE")),
		secretPath:  envOr("AFC_SECRET_FILE_PATH", "fabric/state/secrets.json"),
		keyVaultURL: os.Getenv("AFC_KEY_VAULT_URL"),
		tokenTmpDir: envOr("AFC_TMP_DIR", os.TempDir()),
		callTmpDir:  envOr("AFC_TMP_DIR", os.TempDir()),
	}
}

func buildOnelakeURI(workspace, lakehouse string) string {
	if workspace == "" || lakehouse == "" {
		return ""
	}
	return fmt.Sprintf("onelake://%s/%s?use_azure_default_credential=true", workspace, lakehouse)
}

func main() {
	os.Exit(run())
}

func run() int {
	configFlag := flag.String("config", "", "path to entities.yaml (overrides AFC_ENTITIES_CONFIG)")
	flag.Parse()

	logger := log.New(os.Stdout, "", 0)
	env := loadRunnerEnv()
	if *configFlag != "" {
		env.configPath = *configFlag
	}

	cfg, err := LoadConfig(env.configPath)
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return exitEntityFailed
	}

	store, err := NewSecretStore(cfg.Runner.SecretStore, env.secretPath, env.keyVaultURL)
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return exitEntityFailed
	}

	st, err := LoadState(env.statePath)
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return exitEntityFailed
	}

	lock, err := AcquireLock(env.lockPath)
	if err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		return exitEntityFailed
	}
	defer func() { _ = lock.Release() }()

	now := time.Now().UTC()
	projected := projectedMonthEndCalls(now, st.CallsThisMonth(now))
	overBudget := budgetExceeded(projected, cfg.Runner.MonthlyCallLimit, cfg.Runner.BudgetThresholdFraction)
	if overBudget {
		logger.Printf(
			`{"level":"warn","msg":"projected month-end usage exceeds threshold","calls_this_month":%d,"projected":%d,"monthly_limit":%d}`,
			st.CallsThisMonth(now), int64(projected), cfg.Runner.MonthlyCallLimit,
		)
	}

	exitCode := exitOK
	anyFailed := false

	for _, e := range cfg.Entities {
		if env.scheduleOnly != "" && e.ScheduleGroup != env.scheduleOnly {
			continue
		}
		if !shouldRunEntity(e, overBudget) {
			logger.Printf(`{"entity":%q,"status":"skipped","reason":"budget"}`, e.Table)
			continue
		}

		start := time.Now()
		calls, runErr := runEntity(context.Background(), env, cfg.Runner.WatermarkOverlap.AsDuration(), st, e)
		duration := time.Since(start)

		st.AddCalls(now, calls)

		if runErr != nil {
			anyFailed = true
			logger.Printf(
				`{"entity":%q,"status":"error","duration_ms":%d,"calls":%d,"error":%q}`,
				e.Table, duration.Milliseconds(), calls, runErr.Error(),
			)
			continue // watermark is not advanced on failure (section 9.3.3)
		}

		st.Watermarks[e.Table] = now
		logger.Printf(
			`{"entity":%q,"status":"ok","duration_ms":%d,"calls":%d}`,
			e.Table, duration.Milliseconds(), calls,
		)

		if err := st.Save(env.statePath); err != nil {
			logger.Printf(`{"level":"error","msg":%q}`, fmt.Sprintf("saving state after %s: %v", e.Table, err))
			anyFailed = true
		}

		if err := rotateTokenIfPresent(context.Background(), store, env, e); err != nil {
			logger.Printf(`{"level":"error","msg":%q}`, fmt.Sprintf("rotating token after %s: %v", e.Table, err))
			anyFailed = true
		}
	}

	if err := st.Save(env.statePath); err != nil {
		logger.Printf(`{"level":"error","msg":%q}`, err.Error())
		anyFailed = true
	}

	if anyFailed {
		exitCode = exitEntityFailed
	} else if overBudget {
		exitCode = exitBudgetWarning
	}
	return exitCode
}

// runEntity invokes ingestr for one entity and returns the number of
// Bullhorn API calls it made (best-effort; see parseCallCount).
//
// Integration point: pkg/source/bullhorn's Close writes its call count, as a
// bare integer, to the file named by AFC_CALL_COUNT_FILE (confirmed against
// the source implementation), and its rotated refresh token to the file
// named by the `token_output` URI parameter (build brief section 6.3.3). The
// subscription requestId cursor is persisted via the source's `state_path`
// table parameter, not `subscription_state` (also confirmed).
func runEntity(ctx context.Context, env runnerEnv, overlap time.Duration, st *State, e EntityConfig) (int64, error) {
	if env.onelakeURI == "" {
		return 0, fmt.Errorf("ONELAKE_WORKSPACE/ONELAKE_LAKEHOUSE not configured")
	}

	refreshToken, err := currentRefreshToken(ctx, env)
	if err != nil {
		return 0, err
	}

	tokenOutPath := filepath.Join(env.tokenTmpDir, fmt.Sprintf("afc-token-%s.json", e.Table))
	callCountPath := filepath.Join(env.callTmpDir, fmt.Sprintf("afc-calls-%s.txt", e.Table))
	defer func() { _ = os.Remove(tokenOutPath) }()
	defer func() { _ = os.Remove(callCountPath) }()

	sourceURI := fmt.Sprintf(
		"bullhorn://?client_id=%s&refresh_token=%s&username=%s&data_center=%s&rate_limit=%s&token_output=%s",
		env.clientID, refreshToken, env.username, env.dataCenter, env.rateLimit, tokenOutPath,
	)

	sourceTable := bullhornSourceTable(e)
	if e.Endpoint == "subscription" {
		subStatePath := filepath.Join(env.tokenTmpDir, "afc-subscription-state.json")
		if st.SubscriptionRequestID != "" {
			if err := os.WriteFile(subStatePath, []byte(st.SubscriptionRequestID), 0o600); err != nil {
				return 0, fmt.Errorf("writing subscription state: %w", err)
			}
		}
		sourceTable += "&state_path=" + subStatePath
		defer func() {
			if data, err := os.ReadFile(subStatePath); err == nil {
				st.SubscriptionRequestID = string(bytes.TrimSpace(data))
			}
			_ = os.Remove(subStatePath)
		}()
	}

	watermark := st.Watermarks[e.Table]
	intervalStart := computeIntervalStart(watermark, seedWatermark(e.Table), overlap)
	intervalEnd := time.Now().UTC()

	args := buildIngestrArgs(e, sourceURI, sourceTable, env.onelakeURI, intervalStart, intervalEnd)

	cmd := exec.CommandContext(ctx, env.ingestrBin, args...)
	cmd.Env = append(os.Environ(),
		"INGESTR_DISABLE_TELEMETRY=true",
		"DISABLE_TELEMETRY=true",
		"AFC_CALL_COUNT_FILE="+callCountPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	calls := int64(0)
	if data, err := os.ReadFile(callCountPath); err == nil {
		if n, ok := parseCallCount(string(data)); ok {
			calls = n
		}
	}

	if runErr != nil {
		return calls, fmt.Errorf("ingestr failed for %s/%s: %w: %s", e.Table, e.Endpoint, runErr, stderr.String())
	}
	return calls, nil
}

func currentRefreshToken(ctx context.Context, env runnerEnv) (string, error) {
	if v := os.Getenv("BULLHORN_REFRESH_TOKEN"); v != "" {
		return v, nil
	}
	// Fall back to whatever the secret store currently holds — this is how a
	// rotated token from a previous entity/run is picked up.
	store, err := NewSecretStore(envOr("AFC_SECRET_STORE_KIND", "file"), env.secretPath, env.keyVaultURL)
	if err != nil {
		return "", err
	}
	return store.GetSecret(ctx, "bullhorn_refresh_token")
}

// rotateTokenIfPresent moves a rotated refresh token from the source's
// token_output file into the configured secret store, so the next entity
// invocation (and the next run) picks it up instead of the now-invalidated
// previous token (build brief section 6.1.4).
func rotateTokenIfPresent(ctx context.Context, store SecretStore, env runnerEnv, e EntityConfig) error {
	tokenOutPath := filepath.Join(env.tokenTmpDir, fmt.Sprintf("afc-token-%s.json", e.Table))
	data, err := os.ReadFile(tokenOutPath)
	if err != nil {
		return nil // no rotation occurred for this entity
	}
	token := string(bytes.TrimSpace(data))
	if token == "" {
		return nil
	}
	return store.SetSecret(ctx, "bullhorn_refresh_token", token)
}

// seedWatermark reads the initial watermark written by fabric/seed/run-seed.sh
// for a table that has not run through the runner before.
func seedWatermark(table string) *time.Time {
	path := filepath.Join("fabric", "state", "seed", table+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw := string(bytes.TrimSpace(data))
	n, err := strconv.ParseInt(raw, 10, 64)
	if err == nil {
		t := time.UnixMilli(n).UTC()
		return &t
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}
