package publication

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationWireTypesUseExactSnakeCaseKeys(t *testing.T) {
	t.Parallel()

	payload := struct {
		Scope      TrustedScope       `json:"scope"`
		Inspection WorktreeInspection `json:"inspection"`
		Plan       ExitPlan           `json:"plan"`
	}{
		Scope: TrustedScope{WorkspaceID: "ws_trusted", WorkspaceRoot: "/trusted/workspace"},
		Inspection: WorktreeInspection{
			Worktree: Worktree{BaseRef: "main"},
			Status:   &WorktreeStatus{HeadSHA: ptr(testHeadSHA)},
		},
		Plan: ExitPlan{
			Actions:          []ExitAction{{BlockedReason: "wait"}},
			GlobalPauseCause: "paused",
			BrowserURL:       "https://example.invalid/compare",
			ForgeStatus:      &ForgeStatus{PRURL: "https://example.invalid/pull/1"},
			PRPrefill:        &PRPrefill{Title: "title"},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, key := range []string{
		`"workspace_id"`, `"workspace_root"`, `"base_ref"`, `"blocked_reason"`,
		`"global_pause_cause"`, `"browser_url"`, `"forge_status"`, `"pr_prefill"`, `"head_sha"`,
	} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("encoded payload %s does not contain %s", encoded, key)
		}
	}
}


func ptr[T any](value T) *T { return &value }

func absoluteTestPath(parts ...string) string {
	return filepath.Join(append([]string{string(filepath.Separator)}, parts...)...)
}

const testHeadSHA = "0123456789abcdef0123456789abcdef01234567"

const statusFixtureJSON = `{
  "worktree_id": "wt_delivery",
  "status": {
    "branch": "feature/delivery",
    "detached": false,
    "head_sha": "0123456789abcdef0123456789abcdef01234567",
    "dirty_files": 0,
    "has_upstream": false,
    "ahead": null,
    "ahead_of_base": 1,
    "behind": null
  }
}`

const inspectFixtureJSON = `{
  "worktree": {
    "id": "wt_delivery",
    "workspace_id": "ws_trusted",
    "branch": "feature/delivery",
    "path": "/trusted/workspace/.worktrees/delivery",
    "state": "ready",
    "base_ref": "main"
  },
  "status": {
    "branch": "feature/delivery",
    "detached": false,
    "head_sha": "0123456789abcdef0123456789abcdef01234567",
    "dirty_files": 0,
    "has_upstream": false,
    "ahead": null,
    "ahead_of_base": 1,
    "behind": null
  },
  "forge": {"provider": "github", "pr_url": ""},
  "repo": {"git_backed": true, "git_available": true}
}`

const exitFixtureJSON = `{
  "worktree_id": "wt_delivery",
  "primary": "push",
  "actions": [
    {"action": "push", "enabled": true, "publish": true},
    {"action": "open_pr", "enabled": false, "blocked_reason": "Push commits before opening pull requests."}
  ],
  "forge": {"provider": "github", "default_branch": "main"},
  "forge_status": {"provider": "github", "pr_url": ""},
  "pr_prefill": {"title": "Feature title", "body": "Feature body"},
  "base": "main"
}`
