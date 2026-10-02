package questions

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/batuta-ai/core/routing"
)

const maxPassageBytes = 4000

var (
	secretLine      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)
	contextTaskLine = regexp.MustCompile(`^(?:\*\*Task ([0-9]+)\.\*\*|\*\*Tasks ([0-9]+(?:\s*[–-]\s*[0-9]+)?(?:\s*,\s*[0-9]+(?:\s*[–-]\s*[0-9]+)?)*)\.\*\*)`)
)

type passageParagraph struct {
	text     string
	labelled bool
}

// Passage carries only the task's own plan text to the question match judge.
func Passage(plan routing.Plan, task routing.PlanTask) string {
	lines := []string{task.Title}
	for _, entry := range task.Scope {
		if entry = dropSecretLines(entry); entry != "" {
			lines = append(lines, "Scope: "+entry)
		}
	}
	for _, entry := range task.Accept {
		if entry = dropSecretLines(entry); entry != "" {
			lines = append(lines, "Accept: "+entry)
		}
	}
	text := strings.Join(lines, "\n")
	context := boundContext(plan.ContextFor(task.Number))
	if context != "" {
		text += "\n\n" + context
	}
	text = dropSecretLines(text)
	if len(text) <= maxPassageBytes {
		return text
	}
	cut := maxPassageBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// Copied from classify/classify.go's boundContext and dropSecretLines: the
// task's labelled paragraphs precede shared ones, and secret-shaped lines
// are removed before the byte bound is applied. Plan.ContextFor has already
// selected the paragraphs belonging to this task.
func boundContext(context string) string {
	paragraphs := splitPassageParagraphs(dropSecretLines(context))
	labelled := make([]string, 0, len(paragraphs))
	unlabelled := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if paragraph.text == "" {
			continue
		}
		if paragraph.labelled {
			labelled = append(labelled, paragraph.text)
		} else {
			unlabelled = append(unlabelled, paragraph.text)
		}
	}
	return strings.Join(append(labelled, unlabelled...), "\n\n")
}

func dropSecretLines(value string) string {
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if secretLine.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func splitPassageParagraphs(context string) []passageParagraph {
	lines := strings.Split(context, "\n")
	paragraphs := make([]passageParagraph, 0)
	current := make([]string, 0)
	fence := ""
	flush := func() {
		if len(current) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(current, "\n"))
		first := strings.SplitN(text, "\n", 2)[0]
		paragraphs = append(paragraphs, passageParagraph{text: text, labelled: contextTaskLine.MatchString(first)})
		current = current[:0]
	}
	for _, value := range lines {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" && fence == "" {
			flush()
			continue
		}
		current = append(current, value)
		switch {
		case fence == "" && strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case fence == "" && strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		case fence != "" && strings.HasPrefix(trimmed, fence):
			fence = ""
		}
	}
	flush()
	return paragraphs
}
