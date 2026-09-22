package judge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ProviderOff selects no judge, so every caller keeps the deterministic rule.
const ProviderOff Provider = "off"

const (
	configEnvVar         = "BATUTA_JUDGE"
	configFileLimit      = 4 << 10
	minTimeoutMS         = 500
	maxTimeoutMS         = 30_000
	defaultTimeoutMS     = 3_000
	minMaxStateBytes     = 1 << 10
	maxMaxStateBytes     = 200 << 10
	defaultMaxStateBytes = 100_000
)

var keyEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Mode says how a named decision consumes judge verdicts.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeShadow  Mode = "shadow"
	ModeEnforce Mode = "enforce"
)

type DecisionConfig struct {
	Mode      Mode    `json:"mode"`
	Threshold float64 `json:"threshold"`
}

// Config is the parsed judge configuration. It names the environment variable
// that holds the API key and never carries the key itself.
type Config struct {
	Provider      Provider                  `json:"provider"`
	Providers     []Provider                `json:"providers,omitempty"`
	Model         string                    `json:"model"`
	BaseURL       string                    `json:"base_url"`
	KeyEnv        string                    `json:"key_env"`
	TimeoutMS     int                       `json:"timeout_ms"`
	MaxStateBytes int                       `json:"max_state_bytes"`
	Decisions     map[string]DecisionConfig `json:"decisions"`
}

// LoadConfig reads .batuta/judge.json under root. BATUTA_JUDGE is "off",
// empty (use the file) or a path to another JSON file, absolute or relative
// to root. A missing default file means the judge is off.
func LoadConfig(root string, getenv func(string) string) (Config, error) {
	override := ""
	if getenv != nil {
		override = strings.TrimSpace(getenv(configEnvVar))
	}
	if override == string(ProviderOff) {
		return Config{Provider: ProviderOff}, nil
	}

	path := filepath.Join(root, ".batuta", "judge.json")
	explicit := override != ""
	if explicit {
		if filepath.IsAbs(override) {
			path = override
		} else {
			path = filepath.Join(root, filepath.FromSlash(override))
		}
	}
	data, err := readConfigFile(path, configFileLimit)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return Config{Provider: ProviderOff}, nil
		}
		return Config{}, err
	}

	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("judge: config must contain one JSON object")
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	config.applyDefaults()
	return config, nil
}

// Decision returns the configuration of a named decision point; an
// unconfigured decision is off.
func (c Config) Decision(name string) DecisionConfig {
	if decision, ok := c.Decisions[name]; ok {
		return decision
	}
	return DecisionConfig{Mode: ModeOff}
}

// Judge builds the HTTP judge, reading the API key from KeyEnv at call time.
// It returns ErrUnavailable with reason judge_off or key_missing.
func (c Config) Judge(getenv func(string) string) (Judge, error) {
	if c.Provider == ProviderOff || c.Provider == "" {
		return nil, unavailable(ReasonJudgeOff, nil)
	}
	if c.Provider == ProviderAuto {
		return c.autoJudge(getenv)
	}
	var key string
	if getenv != nil {
		key = getenv(c.KeyEnv)
	}
	if key == "" {
		return nil, unavailable(ReasonKeyMissing, nil)
	}
	return NewHTTPJudge(Options{
		Provider:      c.Provider,
		BaseURL:       c.BaseURL,
		Model:         c.Model,
		Key:           key,
		Timeout:       time.Duration(c.TimeoutMS) * time.Millisecond,
		MaxStateBytes: c.MaxStateBytes,
	}), nil
}

func (c Config) autoJudge(getenv func(string) string) (Judge, error) {
	order := c.Providers
	if len(order) == 0 {
		order = defaultAutoProviders
	}
	chain := &Chain{order: slices.Clone(order)}
	for _, provider := range order {
		var key string
		if getenv != nil {
			key = getenv(providerKeyEnv(provider))
		}
		if key == "" {
			chain.last = append(chain.last, ChainAttempt{Provider: provider, Reason: ReasonKeyMissing})
			continue
		}
		chain.Judges = append(chain.Judges, Named{
			Provider: provider,
			Judge: NewHTTPJudge(Options{
				Provider:      provider,
				BaseURL:       c.BaseURL,
				Key:           key,
				Timeout:       time.Duration(c.TimeoutMS) * time.Millisecond,
				MaxStateBytes: c.MaxStateBytes,
			}),
		})
	}
	if len(chain.Judges) == 0 {
		return nil, unavailable(ReasonKeyMissing, errors.New("TYPESAFE_API_KEY, AI_GATEWAY_API_KEY, OPENROUTER_API_KEY"))
	}
	return chain, nil
}

func (c Config) validate() error {
	switch c.Provider {
	case ProviderOff:
		if len(c.Providers) > 0 {
			return errors.New("judge: config providers is only valid with provider auto")
		}
		return validateDecisions(c.Decisions)
	case ProviderAuto:
		if c.Model != "" {
			return errors.New("judge: config model is not allowed with provider auto")
		}
		if c.KeyEnv != "" {
			return errors.New("judge: config key_env is not allowed with provider auto")
		}
		if strings.TrimSpace(c.BaseURL) != "" {
			return errors.New("judge: config base_url is not allowed with provider auto")
		}
		if err := validateAutoProviders(c.Providers); err != nil {
			return err
		}
	case ProviderTypesafe, ProviderOpenRouter, ProviderVercel:
		if len(c.Providers) > 0 {
			return errors.New("judge: config providers is only valid with provider auto")
		}
	default:
		if c.Provider == "" {
			return errors.New("judge: config provider is required")
		}
		return fmt.Errorf("judge: config provider %q is unknown", c.Provider)
	}
	if c.Provider != ProviderAuto && c.Model == "" {
		return errors.New("judge: config model is required")
	}
	if c.KeyEnv != "" && !keyEnvPattern.MatchString(c.KeyEnv) {
		return fmt.Errorf("judge: config key_env %q must match %s", c.KeyEnv, keyEnvPattern)
	}
	if c.TimeoutMS != 0 && (c.TimeoutMS < minTimeoutMS || c.TimeoutMS > maxTimeoutMS) {
		return fmt.Errorf("judge: config timeout_ms %d must be between %d and %d", c.TimeoutMS, minTimeoutMS, maxTimeoutMS)
	}
	if c.MaxStateBytes != 0 && (c.MaxStateBytes < minMaxStateBytes || c.MaxStateBytes > maxMaxStateBytes) {
		return fmt.Errorf("judge: config max_state_bytes %d must be between %d and %d", c.MaxStateBytes, minMaxStateBytes, maxMaxStateBytes)
	}
	return validateDecisions(c.Decisions)
}

func validateDecisions(decisions map[string]DecisionConfig) error {
	for name, decision := range decisions {
		switch decision.Mode {
		case ModeOff, ModeShadow, ModeEnforce:
		default:
			return fmt.Errorf("judge: decisions.%s mode %q must be off, shadow or enforce", name, decision.Mode)
		}
		if decision.Threshold < 0 || decision.Threshold > 1 {
			return fmt.Errorf("judge: decisions.%s threshold %g must be between 0 and 1", name, decision.Threshold)
		}
	}
	return nil
}

func validateAutoProviders(providers []Provider) error {
	seen := make(map[Provider]struct{}, len(providers))
	for _, provider := range providers {
		switch provider {
		case ProviderTypesafe, ProviderOpenRouter, ProviderVercel:
		default:
			return fmt.Errorf("judge: config providers %q is unknown", provider)
		}
		if _, ok := seen[provider]; ok {
			return fmt.Errorf("judge: config providers %q is duplicated", provider)
		}
		seen[provider] = struct{}{}
	}
	return nil
}

func providerKeyEnv(provider Provider) string {
	switch provider {
	case ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case ProviderVercel:
		return "AI_GATEWAY_API_KEY"
	default:
		return "TYPESAFE_API_KEY"
	}
}

func (c *Config) applyDefaults() {
	if c.KeyEnv == "" && c.Provider != ProviderAuto {
		c.KeyEnv = providerKeyEnv(c.Provider)
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = defaultTimeoutMS
	}
	if c.MaxStateBytes == 0 {
		c.MaxStateBytes = defaultMaxStateBytes
	}
}

func readConfigFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("judge: config file %s is not a regular file or exceeds %d bytes", path, limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("judge: config file %s is not a regular file or exceeds %d bytes", path, limit)
	}
	return data, err
}
