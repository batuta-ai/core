package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/batuta-ai/core/routing"
)

// grantDigest identifies the normalized standing grant a reply was answered from.
func grantDigest(grant routing.TaskGrant) string {
	canonical, err := json.Marshal(grant.Normalize())
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])[:12]
}

func grantQuestionKey(question string) string {
	return strings.Join(strings.Fields(question), " ")
}

func grantRepliedDetail(execution int, runID, question, requestID string, grant routing.TaskGrant, turn int) map[string]any {
	return map[string]any{
		"execution": execution, "run_id": runID, "question": question, "request_id": requestID,
		"grant_digest": grantDigest(grant), "turn": turn,
	}
}

// grantTurnSuffix leaves turn 0 on the paths an execution has always used.
func grantTurnSuffix(execution, turn int) string {
	suffix := "-e" + fmt.Sprint(execution)
	if turn > 0 {
		suffix += "-g" + fmt.Sprint(turn)
	}
	return suffix
}

func (r *Runner) grantTurnBriefPath(taskID string, execution, turn int) string {
	return strings.TrimSuffix(r.trailPath(taskID), ".md") + grantTurnSuffix(execution, turn) + ".brief.md"
}

func (r *Runner) grantTurnLogPath(taskID string, execution, turn int) string {
	return strings.TrimSuffix(r.trailPath(taskID), ".md") + grantTurnSuffix(execution, turn) + ".out.log"
}
