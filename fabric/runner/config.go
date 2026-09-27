package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// EntityConfig configures one Bronze table (build brief section 9.1).
type EntityConfig struct {
	Table          string   `yaml:"table"`
	Entity         string   `yaml:"entity"`
	Endpoint       string   `yaml:"endpoint"` // search | query | meta | subscription
	PrimaryKey     []string `yaml:"primary_key"`
	IncrementalKey string   `yaml:"incremental_key"`
	Strategy       string   `yaml:"strategy"` // merge | replace
	ScheduleGroup  string   `yaml:"schedule_group"`
	Critical       bool     `yaml:"critical"`
	Allowlist      []string `yaml:"allowlist"`

	// Only meaningful for endpoint: meta.
	MetaEntities []string `yaml:"meta_entities"`
	// Only meaningful for endpoint: subscription.
	SubscribedEntities []string `yaml:"subscribed_entities"`
}

// Duration unmarshals a YAML string like "5m" into a time.Duration.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) AsDuration() time.Duration { return time.Duration(d) }

// RunnerSettings configures runner-wide behaviour (build brief section 6.6, 9.3).
type RunnerSettings struct {
	WatermarkOverlap        Duration `yaml:"watermark_overlap"`
	BudgetThresholdFraction float64  `yaml:"budget_threshold_fraction"`
	MonthlyCallLimit        int64    `yaml:"monthly_call_limit"`
	SecretStore             string   `yaml:"secret_store"` // file | keyvault
}

type Config struct {
	Entities []EntityConfig `yaml:"entities"`
	Runner   RunnerSettings `yaml:"runner"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading entities config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing entities config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	seen := make(map[string]bool, len(c.Entities))
	for _, e := range c.Entities {
		if e.Table == "" {
			return fmt.Errorf("entity config: table name is required")
		}
		if seen[e.Table] {
			return fmt.Errorf("entity config: duplicate table %q", e.Table)
		}
		seen[e.Table] = true

		switch e.Endpoint {
		case "search", "query":
			if len(e.Allowlist) == 0 {
				return fmt.Errorf("entity %q: allowlist is required for endpoint %q (wildcard field selection is not permitted)", e.Table, e.Endpoint)
			}
			for _, f := range e.Allowlist {
				if f == "*" {
					return fmt.Errorf("entity %q: wildcard field %q is not permitted in allowlist", e.Table, f)
				}
			}
		case "meta":
			if len(e.MetaEntities) == 0 {
				return fmt.Errorf("entity %q: meta_entities is required for endpoint meta", e.Table)
			}
		case "subscription":
			if len(e.SubscribedEntities) == 0 {
				return fmt.Errorf("entity %q: subscribed_entities is required for endpoint subscription", e.Table)
			}
		default:
			return fmt.Errorf("entity %q: unknown endpoint %q", e.Table, e.Endpoint)
		}

		switch e.Strategy {
		case "merge", "replace":
		default:
			return fmt.Errorf("entity %q: unknown strategy %q", e.Table, e.Strategy)
		}
	}
	if c.Runner.MonthlyCallLimit <= 0 {
		return fmt.Errorf("runner: monthly_call_limit must be positive")
	}
	if c.Runner.BudgetThresholdFraction <= 0 || c.Runner.BudgetThresholdFraction > 1 {
		return fmt.Errorf("runner: budget_threshold_fraction must be in (0, 1]")
	}
	switch c.Runner.SecretStore {
	case "file", "keyvault":
	default:
		return fmt.Errorf("runner: unknown secret_store %q", c.Runner.SecretStore)
	}
	return nil
}
