package adapters

import (
	"strings"

	"github.com/batuta-ai/core/inventory"
)

func NewAgy(executable string) (Adapter, error) {
	ids := map[string]inventory.ProbeID{
		"version": "agy.version",
		"agents":  "agy.agents",
		"plugins": "agy.plugins",
		"models":  "agy.models",
	}
	args := map[string][]string{
		"version": {"--version"},
		"agents":  {"agent"},
		"plugins": {"plugin", "list"},
		"models":  {"models"},
	}
	return orderedAdapter(inventory.ExecutorAgy, executable, []string{"version", "agents", "plugins", "models"}, ids, args, "version", "plugins", func(outputs map[inventory.ProbeID][]byte) inventory.ExecutorSnapshot {
		return normalizeAgy(ids, outputs)
	})
}

func normalizeAgy(ids map[string]inventory.ProbeID, outputs map[inventory.ProbeID][]byte) inventory.ExecutorSnapshot {
	version, versionOK := versionEvidence(outputs[ids["version"]], "agy --version", "")
	snapshot := inventory.ExecutorSnapshot{
		ID: inventory.ExecutorAgy, Version: version, Diagnostics: diagnosticForVersion(versionOK),
		CredentialState: inventory.CredentialUnknown,
	}
	for _, entry := range []struct {
		key, name, source string
	}{
		{key: "agents", name: "agents", source: "agy agent"},
		{key: "plugins", name: "plugins", source: "agy plugin list"},
	} {
		raw := outputs[ids[entry.key]]
		identifiers := safeLineIdentifiers(raw)
		if len(identifiers) == 0 {
			snapshot.Capabilities = append(snapshot.Capabilities, unknownEvidence(entry.name, entry.source, "probe_unavailable"))
			continue
		}
		snapshot.Capabilities = append(snapshot.Capabilities, evidence(entry.name, entry.source, inventory.ResolutionResolved, raw, identifiers))
	}
	modelRaw := outputs[ids["models"]]
	models := agyModelIdentifiers(modelRaw)
	if len(models) > 0 {
		snapshot.ProviderBindings = []inventory.ProviderBinding{{ProviderID: "agy"}}
		for _, model := range models {
			snapshot.ProviderBindings = append(snapshot.ProviderBindings, inventory.ProviderBinding{ProviderID: "agy", ModelID: model})
		}
		snapshot.Capabilities = append(snapshot.Capabilities, evidence("models", "agy models", inventory.ResolutionResolved, modelRaw, models))
	} else {
		snapshot.Capabilities = append(snapshot.Capabilities, unknownEvidence("models", "agy models", "probe_unavailable"))
	}
	appendSkew(&snapshot, schemaSkewed(outputs[ids["plugins"]]))
	return snapshot
}

// agyModelIdentifiers reads `agy models`: one `<id><TAB><display name>` line
// per model after a "Fetching available models..." banner. Only the id is
// kept, and only when it is a safe public identifier.
func agyModelIdentifiers(raw []byte) []string {
	models := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		id, _, found := strings.Cut(strings.TrimSpace(line), "\t")
		id = strings.TrimSpace(id)
		if !found || !safePublicIdentifier(id) {
			continue
		}
		models = append(models, id)
	}
	return cleanIdentifiers(models)
}

func safeLineIdentifiers(raw []byte) []string {
	values := make([]string, 0)
	for _, value := range nonemptyLines(raw) {
		if safePublicIdentifier(value) {
			values = append(values, value)
		}
	}
	return cleanIdentifiers(values)
}
