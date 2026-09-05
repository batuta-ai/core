package adapters

import (
	"encoding/json"

	"github.com/batuta-ai/core/inventory"
)

func NewCodex(executable string) (Adapter, error) {
	// `debug models` lists what the signed-in account can use; `--bundled`
	// is the list compiled into the binary and only a fallback — the two
	// differ (gpt-6-astra is account-only, gpt-5.2 is bundled-only).
	ids := map[string]inventory.ProbeID{"version": "codex.version", "doctor": "codex.doctor", "mcp": "codex.mcp", "plugins": "codex.plugins", "marketplaces": "codex.marketplaces", "models": "codex.models", "models_bundled": "codex.models_bundled"}
	args := map[string][]string{
		"version": {"--version"}, "doctor": {"doctor", "--json", "--summary"}, "mcp": {"mcp", "list", "--json"},
		"plugins": {"plugin", "list", "--json"}, "marketplaces": {"plugin", "marketplace", "list", "--json"},
		"models": {"debug", "models"}, "models_bundled": {"debug", "models", "--bundled"},
	}
	order := []string{"version", "doctor", "mcp", "plugins", "marketplaces", "models", "models_bundled"}
	return orderedAdapter(inventory.ExecutorCodex, executable, order, ids, args, "version", "doctor", func(outputs map[inventory.ProbeID][]byte) inventory.ExecutorSnapshot {
		return normalizeCodex(ids, outputs)
	})
}

// codexModelSlugs returns the safe slugs of a `codex debug models` payload,
// or nil when the payload is absent or not the expected JSON.
func codexModelSlugs(raw []byte) []string {
	var models struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &models) != nil {
		return nil
	}
	slugs := make([]string, 0, len(models.Models))
	for _, model := range models.Models {
		if safePublicIdentifier(model.Slug) {
			slugs = append(slugs, model.Slug)
		}
	}
	return slugs
}

func normalizeCodex(ids map[string]inventory.ProbeID, outputs map[inventory.ProbeID][]byte) inventory.ExecutorSnapshot {
	version, versionOK := versionEvidence(outputs[ids["version"]], "codex --version", "")
	snapshot := inventory.ExecutorSnapshot{ID: inventory.ExecutorCodex, Version: version, Diagnostics: diagnosticForVersion(versionOK)}
	snapshot.ProviderBindings = append(snapshot.ProviderBindings, inventory.ProviderBinding{ProviderID: "codex"})
	raw, source, slugs := outputs[ids["models"]], "codex debug models", codexModelSlugs(outputs[ids["models"]])
	if slugs == nil {
		raw, source, slugs = outputs[ids["models_bundled"]], "codex debug models --bundled (account list unavailable)", codexModelSlugs(outputs[ids["models_bundled"]])
	}
	if slugs != nil {
		identifiers := make([]string, 0, len(slugs))
		for _, slug := range slugs {
			identifiers = append(identifiers, "codex/"+slug)
			snapshot.ProviderBindings = append(snapshot.ProviderBindings, inventory.ProviderBinding{ProviderID: "codex", ModelID: slug})
		}
		snapshot.Capabilities = append(snapshot.Capabilities, evidence("models", source, inventory.ResolutionResolved, raw, identifiers))
	} else {
		snapshot.Capabilities = append(snapshot.Capabilities, unknownEvidence("models", "codex debug models", "probe_unavailable"))
	}
	snapshot.Capabilities = append(snapshot.Capabilities, evidence("config", "CODEX_HOME config", inventory.ResolutionDeclared, nil, nil))
	for _, entry := range []struct{ key, name string }{{"mcp", "mcp"}, {"plugins", "plugins"}, {"marketplaces", "marketplaces"}} {
		raw := outputs[ids[entry.key]]
		state := inventory.ResolutionUnknown
		if len(raw) > 0 && json.Valid(raw) {
			state = inventory.ResolutionDeclared
		}
		snapshot.Capabilities = append(snapshot.Capabilities, evidence(entry.name, "codex "+entry.key, state, raw, nil))
	}
	snapshot.CredentialState = inventory.CredentialUnknown
	appendSkew(&snapshot, schemaSkewed(outputs[ids["doctor"]]))
	return snapshot
}
