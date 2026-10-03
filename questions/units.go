package questions

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxUnitBytes = 400

// Units splits a Passage into the short units v2 scores one call at a time:
// each Scope line, each semicolon-separated Accept criterion, and each
// sentence or line of the context paragraphs. The title line is never a unit.
func Units(passage string) []string {
	lines := strings.Split(passage, "\n")
	var units []string
	inHeader := true
	for _, line := range lines[1:] {
		if inHeader && strings.TrimSpace(line) == "" {
			inHeader = false
			continue
		}
		switch {
		case inHeader && strings.HasPrefix(line, "Scope: "):
			units = appendUnit(units, "Scope: ", strings.TrimPrefix(line, "Scope: "))
		case inHeader && strings.HasPrefix(line, "Accept: "):
			for _, criterion := range strings.Split(strings.TrimPrefix(line, "Accept: "), ";") {
				units = appendUnit(units, "Accept: ", criterion)
			}
		default:
			for _, sentence := range splitSentences(line) {
				units = appendUnit(units, "", sentence)
			}
		}
	}
	return units
}

// UnitPacket is the passage the judge sees for one unit: the title line, a
// line break and the unit.
func UnitPacket(passage, unit string) string {
	title, _, _ := strings.Cut(passage, "\n")
	return title + "\n" + unit
}

func appendUnit(units []string, prefix, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return units
	}
	return append(units, boundUnit(prefix+text))
}

func boundUnit(unit string) string {
	if len(unit) <= maxUnitBytes {
		return unit
	}
	cut := maxUnitBytes
	for cut > 0 && !utf8.RuneStart(unit[cut]) {
		cut--
	}
	return unit[:cut]
}

func splitSentences(text string) []string {
	var sentences []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '.' && text[i] != '!' && text[i] != '?' {
			continue
		}
		next, size := utf8.DecodeRuneInString(text[i+1:])
		if i+1 < len(text) && unicode.IsSpace(next) {
			sentences = append(sentences, text[start:i+1])
			start = i + 1 + size
		}
	}
	return append(sentences, text[start:])
}
