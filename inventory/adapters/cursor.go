package adapters

import (
	"strings"

	"github.com/batuta-ai/core/inventory"
)

func NewCursor(executable string) (Adapter, error) {
	ids := map[string]inventory.ProbeID{"version": "cursor.version", "status": "cursor.status", "models": "cursor.models"}
	args := map[string][]string{"version": {"--version"}, "status": {"status"}, "models": {"models"}}
	order := []string{"version", "status", "models"}
	a, err := orderedAdapter(inventory.ExecutorCursorAgent, executable, order, ids, args, "version", "status", func(outputs map[inventory.ProbeID][]byte) inventory.ExecutorSnapshot {
		return normalizeCursor(ids, outputs)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func normalizeCursor(ids map[string]inventory.ProbeID, outputs map[inventory.ProbeID][]byte) inventory.ExecutorSnapshot {
	version, versionOK := versionEvidence(outputs[ids["version"]], "agent --version", "")
	snapshot := inventory.ExecutorSnapshot{ID: inventory.ExecutorCursorAgent, Version: version, Diagnostics: diagnosticForVersion(versionOK)}
	snapshot.ProviderBindings = []inventory.ProviderBinding{{ProviderID: "cursor"}}
	modelRaw := outputs[ids["models"]]
	models := make([]string, 0)
	for _, line := range strings.Split(string(modelRaw), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), " - ", 2)
		if len(parts) == 2 && safePublicIdentifier(parts[0]) {
			models = append(models, "cursor/"+strings.TrimSpace(parts[0]))
		}
	}
	if len(models) > 0 {
		for _, model := range models {
			snapshot.ProviderBindings = append(snapshot.ProviderBindings, inventory.ProviderBinding{ProviderID: "cursor", ModelID: strings.TrimPrefix(model, "cursor/")})
		}
		snapshot.Capabilities = append(snapshot.Capabilities, evidence("models", "agent models", inventory.ResolutionResolved, modelRaw, models))
	} else {
		snapshot.Capabilities = append(snapshot.Capabilities, unknownEvidence("models", "agent models", "probe_unavailable"))
	}
	snapshot.Capabilities = append(snapshot.Capabilities, unknownEvidence("config", "cursor CLI", "surface_unavailable"))
	if strings.Contains(strings.ToLower(string(outputs[ids["status"]])), "auth") {
		snapshot.CredentialState = inventory.CredentialConfigured
	} else {
		snapshot.CredentialState = inventory.CredentialUnknown
	}
	appendSkew(&snapshot, schemaSkewed(outputs[ids["status"]]))
	return snapshot
}
