package loop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

// QuestionTarget identifies the exact native question shown to a caller.
// OpenedDigest is the first delivery_opened journal record's Digest.
type QuestionTarget struct {
	DeliveryID   string
	TaskID       string
	Execution    int
	QuestionID   string
	OpenedDigest string
}

var (
	ErrDeliveryOwned         = errors.New("loop: delivery is owned")
	ErrInvalidQuestionTarget = errors.New("loop: invalid question target")
	ErrStaleQuestion         = errors.New("loop: the shown question is no longer waiting for an answer")
)

// AnswerQuestion records an answer only if the shown opening and execution
// still own the pending question. A nonempty delivery with an error means the
// answer was appended but cleanup failed. An append error can have uncertain
// effect; callers must inspect the journal rather than blindly retry.
func AnswerQuestion(ctx context.Context, workspace string, target QuestionTarget, text string) (string, error) {
	if !filepath.IsAbs(workspace) || !validQuestionTarget(target) {
		return "", ErrInvalidQuestionTarget
	}
	return answerSelectedContext(ctx, workspace, target.DeliveryID, target.TaskID, target.Execution, target.QuestionID, text, time.Now().UTC(), &target)
}

var questionDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validQuestionTarget(target QuestionTarget) bool {
	return journal.ValidDeliveryID(target.DeliveryID) &&
		target.TaskID != "" && len(target.TaskID) <= 256 && strings.TrimSpace(target.TaskID) == target.TaskID && !strings.ContainsAny(target.TaskID, "\x00\r\n") &&
		target.Execution > 0 && target.Execution <= routing.MaxTaskExecutions &&
		validQuestionDigest(target.QuestionID) && validQuestionDigest(target.OpenedDigest)
}

func validQuestionDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && questionDigestPattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}

func questionTargetMatches(root string, records []journal.Record, task *routing.GraphTask, target QuestionTarget) bool {
	if len(records) == 0 || records[0].Kind != KindOpened || records[0].Digest != target.OpenedDigest || terminalState(records) != "" ||
		task == nil || task.TaskID != target.TaskID || task.State != routing.GraphTaskWaitingInput || len(task.Attempts) == 0 {
		return false
	}
	var opened openedDetail
	if json.Unmarshal(records[0].Detail, &opened) != nil || opened.Workspace != root {
		return false
	}
	for _, record := range records[1:] {
		if record.Kind == KindOpened {
			return false
		}
	}
	attempt := task.Attempts[len(task.Attempts)-1]
	return attempt.Execution == target.Execution && attempt.Question != nil && attempt.Question.RequestID == target.QuestionID
}

// Preserve legacy ownership notices while exposing their meaning to integrations.
type deliveryOwnedError string

func (err deliveryOwnedError) Error() string { return string(err) }
func (err deliveryOwnedError) Unwrap() error { return ErrDeliveryOwned }
