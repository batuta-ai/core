package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validConfig = `{"provider":"typesafe","model":"jev-latest","key_env":"TYPESAFE_API_KEY","timeout_ms":3000,"max_state_bytes":100000,"decisions":{"claim_evidence":{"mode":"shadow","threshold":0.9}}}`

func testGetenv(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

func writeJudgeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoadConfigAbsent(t *testing.T) {
	t.Parallel()

	config, err := LoadConfig(t.TempDir(), testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.Provider != ProviderOff {
		t.Fatalf("Provider = %q, want off", config.Provider)
	}
	if decision := config.Decision("claim_evidence"); decision != (DecisionConfig{Mode: ModeOff}) {
		t.Fatalf("Decision() = %#v, want off with threshold 0", decision)
	}

	_, err = config.Judge(testGetenv(map[string]string{"TYPESAFE_API_KEY": "sk-test"}))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Judge() error = %v, want ErrUnavailable", err)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != ReasonJudgeOff {
		t.Fatalf("Judge() error = %v, want reason judge_off", err)
	}
}

func TestLoadConfigEnv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), validConfig)

	config, err := LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.Provider != ProviderTypesafe || config.Model != "jev-latest" || config.KeyEnv != "TYPESAFE_API_KEY" {
		t.Fatalf("config = %#v", config)
	}
	if config.TimeoutMS != 3000 || config.MaxStateBytes != 100000 {
		t.Fatalf("config = %#v", config)
	}
	if decision := config.Decision("claim_evidence"); decision.Mode != ModeShadow || decision.Threshold != 0.9 {
		t.Fatalf("Decision(claim_evidence) = %#v", decision)
	}

	envConfig, err := LoadConfig(root, testGetenv(map[string]string{"BATUTA_JUDGE": "off"}))
	if err != nil {
		t.Fatalf("LoadConfig(off) error = %v", err)
	}
	if envConfig.Provider != ProviderOff {
		t.Fatalf("BATUTA_JUDGE=off Provider = %q, want off", envConfig.Provider)
	}
	_, err = envConfig.Judge(testGetenv(map[string]string{"TYPESAFE_API_KEY": "sk-test"}))
	requireUnavailable(t, err, ReasonJudgeOff)

	writeJudgeConfig(t, filepath.Join(root, "custom", "judge.json"), `{"provider":"openrouter","model":"typesafe/jev-1.13"}`)
	envConfig, err = LoadConfig(root, testGetenv(map[string]string{"BATUTA_JUDGE": "custom/judge.json"}))
	if err != nil {
		t.Fatalf("LoadConfig(relative) error = %v", err)
	}
	if envConfig.Provider != ProviderOpenRouter || envConfig.Model != "typesafe/jev-1.13" || envConfig.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("relative override config = %#v", envConfig)
	}

	absFile := filepath.Join(t.TempDir(), "other.json")
	writeJudgeConfig(t, absFile, `{"provider":"typesafe","model":"jev-latest"}`)
	envConfig, err = LoadConfig(root, testGetenv(map[string]string{"BATUTA_JUDGE": absFile}))
	if err != nil {
		t.Fatalf("LoadConfig(absolute) error = %v", err)
	}
	if envConfig.Provider != ProviderTypesafe {
		t.Fatalf("absolute override Provider = %q, want typesafe", envConfig.Provider)
	}

	if _, err := LoadConfig(root, testGetenv(map[string]string{"BATUTA_JUDGE": "missing/judge.json"})); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadConfig(missing override) error = %v, want ErrNotExist", err)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	t.Parallel()

	full := `{"provider":"typesafe","model":"m"`
	cases := map[string]struct {
		content string
		wantErr string
	}{
		"unknown field":         {content: full + `,"provider_name":"x"}`, wantErr: "unknown field"},
		"second value":          {content: full + `}` + "\n{}", wantErr: "one JSON object"},
		"over 4 KiB":            {content: full + `,"pad":"` + strings.Repeat("a", 5000) + `"}`, wantErr: "exceeds 4096"},
		"unknown provider":      {content: `{"provider":"anthropic","model":"m"}`, wantErr: "provider"},
		"empty provider":        {content: `{}`, wantErr: "provider"},
		"invalid key_env":       {content: `{"provider":"typesafe","model":"m","key_env":"typesafe-key"}`, wantErr: "key_env"},
		"timeout below range":   {content: full + `,"timeout_ms":499}`, wantErr: "timeout_ms"},
		"timeout above range":   {content: full + `,"timeout_ms":30001}`, wantErr: "timeout_ms"},
		"state below range":     {content: full + `,"max_state_bytes":1023}`, wantErr: "max_state_bytes"},
		"state above range":     {content: full + `,"max_state_bytes":204801}`, wantErr: "max_state_bytes"},
		"unknown decision mode": {content: full + `,"decisions":{"d":{"mode":"yolo","threshold":0.9}}}`, wantErr: "mode"},
		"threshold above one":   {content: full + `,"decisions":{"d":{"mode":"shadow","threshold":1.5}}}`, wantErr: "threshold"},
		"threshold below zero":  {content: full + `,"decisions":{"d":{"mode":"shadow","threshold":-0.1}}}`, wantErr: "threshold"},
		"null document":         {content: `null`, wantErr: "provider"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), tc.content)
			_, err := LoadConfig(root, testGetenv(nil))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadConfig() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	boundary := full + `,"timeout_ms":30000,"max_state_bytes":204800,"decisions":{"d":{"mode":"enforce","threshold":1}}}`
	padded := validConfig + strings.Repeat(" ", 4096-len(validConfig))
	for name, content := range map[string]string{"at bounds": boundary, "at size limit": padded} {
		content := content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), content)
			config, err := LoadConfig(root, testGetenv(nil))
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if config.TimeoutMS == 30000 && config.MaxStateBytes != 204800 {
				t.Fatalf("config = %#v", config)
			}
		})
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), `{"provider":"typesafe","model":"m"}`)
	config, err := LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.KeyEnv != "TYPESAFE_API_KEY" || config.TimeoutMS != 10000 || config.MaxStateBytes != 100000 {
		t.Fatalf("defaults not applied: %#v", config)
	}

	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), `{"provider":"openrouter","model":"m"}`)
	config, err = LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q, want OPENROUTER_API_KEY", config.KeyEnv)
	}
}

func TestLoadConfigModelDefaults(t *testing.T) {
	t.Parallel()

	defaults := map[Provider]string{
		ProviderTypesafe:   "jev-latest",
		ProviderVercel:     "typesafe-ai/jev",
		ProviderOpenRouter: "typesafe/jev-1.13",
	}
	for provider, want := range defaults {
		provider, want := provider, want
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), fmt.Sprintf(`{"provider":%q}`, provider))
			config, err := LoadConfig(root, testGetenv(nil))
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if config.Model != want {
				t.Fatalf("Model = %q, want %q", config.Model, want)
			}

			writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), fmt.Sprintf(`{"provider":%q,"model":"custom"}`, provider))
			config, err = LoadConfig(root, testGetenv(nil))
			if err != nil {
				t.Fatalf("LoadConfig(explicit model) error = %v", err)
			}
			if config.Model != "custom" {
				t.Fatalf("explicit Model = %q, want custom", config.Model)
			}
		})
	}
}

func TestConfigKeyFromEnv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), validConfig)
	config, err := LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "sk-test") {
		t.Fatalf("config JSON carries the key: %s", encoded)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want Bearer sk-test", got)
		}
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"supported":{"type":"noul","noul":0.93}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)

	probe := config
	probe.BaseURL = srv.URL

	var asked []string
	judge, err := probe.Judge(func(name string) string {
		asked = append(asked, name)
		return "sk-test"
	})
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if len(asked) < 1 || asked[0] != "TYPESAFE_API_KEY" {
		t.Fatalf("getenv asked = %v, want TYPESAFE_API_KEY", asked)
	}

	resp, err := judge.Ask(context.Background(), Request{
		State:     "state",
		Questions: map[string]Question{"supported": {Type: QuestionNoul}},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if resp.Answers["supported"].Noul != 0.93 {
		t.Fatalf("noul = %#v", resp.Answers["supported"])
	}

	_, err = probe.Judge(testGetenv(nil))
	requireUnavailable(t, err, ReasonKeyMissing)
}

func TestAutoBuildsChainFromPresentKeys(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), `{"provider":"auto"}`)
	config, err := LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.Provider != ProviderAuto {
		t.Fatalf("Provider = %q, want auto", config.Provider)
	}
	if config.Model != "" || config.KeyEnv != "" || config.BaseURL != "" {
		t.Fatalf("auto config carries per-provider fields: %#v", config)
	}

	built, err := config.Judge(testGetenv(map[string]string{
		"TYPESAFE_API_KEY":   "ts-key",
		"AI_GATEWAY_API_KEY": "gw-key",
		"OPENROUTER_API_KEY": "or-key",
	}))
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	chain, ok := built.(*Chain)
	if !ok {
		t.Fatalf("Judge() type = %T, want *Chain", built)
	}
	if len(chain.Judges) != 3 {
		t.Fatalf("Judges = %#v, want 3", chain.Judges)
	}
	wantOrder := []Provider{ProviderTypesafe, ProviderVercel, ProviderOpenRouter}
	for i, want := range wantOrder {
		if chain.Judges[i].Provider != want {
			t.Fatalf("Judges[%d].Provider = %q, want %q", i, chain.Judges[i].Provider, want)
		}
		if _, ok := chain.Judges[i].Judge.(*HTTPJudge); !ok {
			t.Fatalf("Judges[%d].Judge type = %T, want *HTTPJudge", i, chain.Judges[i].Judge)
		}
	}

	first := Response{Model: "typesafe-answer", Answers: map[string]Answer{"ok": {Type: QuestionNoul, Noul: 0.9}}}
	chain.Judges[0].Judge = staticJudge{resp: first}
	chain.Judges[1].Judge = staticJudge{resp: Response{Model: "vercel-should-not-run"}}
	chain.Judges[2].Judge = staticJudge{resp: Response{Model: "openrouter-should-not-run"}}

	resp, err := chain.Ask(context.Background(), Request{
		State:     "state",
		Questions: map[string]Question{"ok": {Type: QuestionNoul}},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if resp.Model != "typesafe-answer" {
		t.Fatalf("Ask() Model = %q, want typesafe-answer", resp.Model)
	}
}

func TestAutoWithoutKeys(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), `{"provider":"auto"}`)
	config, err := LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	built, err := config.Judge(testGetenv(map[string]string{"OPENROUTER_API_KEY": "or-key"}))
	if err != nil {
		t.Fatalf("Judge(openrouter only) error = %v", err)
	}
	chain, ok := built.(*Chain)
	if !ok {
		t.Fatalf("Judge() type = %T, want *Chain", built)
	}
	if len(chain.Judges) != 1 || chain.Judges[0].Provider != ProviderOpenRouter {
		t.Fatalf("Judges = %#v, want a single openrouter entry", chain.Judges)
	}

	_, err = config.Judge(testGetenv(nil))
	requireUnavailable(t, err, ReasonKeyMissing)
	for _, name := range []string{"TYPESAFE_API_KEY", "AI_GATEWAY_API_KEY", "OPENROUTER_API_KEY"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("Judge() error = %v, want %s named", err, name)
		}
	}

	for name, content := range map[string]string{
		"model":    `{"provider":"auto","model":"jev-latest"}`,
		"key_env":  `{"provider":"auto","key_env":"TYPESAFE_API_KEY"}`,
		"base_url": `{"provider":"auto","base_url":"https://example.invalid"}`,
	} {
		name, content := name, content
		t.Run("rejects "+name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeJudgeConfig(t, filepath.Join(dir, ".batuta", "judge.json"), content)
			_, err := LoadConfig(dir, testGetenv(nil))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("LoadConfig() error = %v, want containing %q", err, name)
			}
		})
	}
}

func TestProvidersListOrder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), `{"provider":"auto","providers":["openrouter","typesafe"]}`)
	config, err := LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !reflect.DeepEqual(config.Providers, []Provider{ProviderOpenRouter, ProviderTypesafe}) {
		t.Fatalf("Providers = %#v", config.Providers)
	}

	built, err := config.Judge(testGetenv(map[string]string{
		"TYPESAFE_API_KEY":   "ts-key",
		"OPENROUTER_API_KEY": "or-key",
	}))
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	chain, ok := built.(*Chain)
	if !ok {
		t.Fatalf("Judge() type = %T, want *Chain", built)
	}
	if len(chain.Judges) != 2 || chain.Judges[0].Provider != ProviderOpenRouter || chain.Judges[1].Provider != ProviderTypesafe {
		t.Fatalf("Judges = %#v, want openrouter then typesafe", chain.Judges)
	}

	cases := map[string]string{
		"unknown":   `{"provider":"auto","providers":["typesafe","anthropic"]}`,
		"duplicate": `{"provider":"auto","providers":["typesafe","vercel","typesafe"]}`,
	}
	for name, content := range cases {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeJudgeConfig(t, filepath.Join(dir, ".batuta", "judge.json"), content)
			_, err := LoadConfig(dir, testGetenv(nil))
			if err == nil {
				t.Fatal("LoadConfig() error = nil, want rejection")
			}
			if name == "unknown" && !strings.Contains(err.Error(), "anthropic") {
				t.Fatalf("LoadConfig() error = %v, want unknown name", err)
			}
			if name == "duplicate" && !strings.Contains(err.Error(), "typesafe") {
				t.Fatalf("LoadConfig() error = %v, want duplicate name", err)
			}
		})
	}

	writeJudgeConfig(t, filepath.Join(root, ".batuta", "judge.json"), `{"provider":"auto","providers":["typesafe","vercel","openrouter"]}`)
	config, err = LoadConfig(root, testGetenv(nil))
	if err != nil {
		t.Fatalf("LoadConfig(full list) error = %v", err)
	}
	built, err = config.Judge(testGetenv(map[string]string{"AI_GATEWAY_API_KEY": "gw-key"}))
	if err != nil {
		t.Fatalf("Judge(vercel only) error = %v", err)
	}
	chain, ok = built.(*Chain)
	if !ok {
		t.Fatalf("Judge() type = %T, want *Chain", built)
	}
	if len(chain.Judges) != 1 || chain.Judges[0].Provider != ProviderVercel {
		t.Fatalf("Judges = %#v, want a single vercel entry", chain.Judges)
	}
	skipped := chain.LastAttempts()
	wantSkipped := []ChainAttempt{
		{Provider: ProviderTypesafe, Reason: ReasonKeyMissing},
		{Provider: ProviderOpenRouter, Reason: ReasonKeyMissing},
	}
	if !reflect.DeepEqual(skipped, wantSkipped) {
		t.Fatalf("LastAttempts() = %#v, want %#v", skipped, wantSkipped)
	}
}
