package loop

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"github.com/batuta-ai/core/journal"
)

const pickerLineLimit = 4 << 20

type deliveryItem struct {
	id          string
	slug        string
	state       string
	presence    string
	opened      time.Time
	updated     time.Time
	description string
}

func (i deliveryItem) Title() string       { return sanitizePanelText(i.id + " · " + i.slug) }
func (i deliveryItem) Description() string { return sanitizePanelText(i.description) }
func (i deliveryItem) FilterValue() string {
	return sanitizePanelText(i.id + " " + i.slug + " " + i.state)
}

func deliveryItems(workspace string, store *journal.Store, now time.Time, style Style) ([]list.Item, error) {
	labels := panelLabels[style.Lang]
	if labels == nil {
		labels = panelLabels["en"]
	}
	renderer := panelRenderer{style: style, g: glyphsFor(style), labels: labels}
	ids, err := store.List()
	if err != nil {
		return nil, err
	}
	items := make([]deliveryItem, 0, len(ids))
	for _, id := range ids {
		first, last, err := deliveryEndpoints(store.Path(id))
		if err != nil || first.Kind != KindOpened {
			continue
		}
		var opened openedDetail
		if json.Unmarshal(first.Detail, &opened) != nil {
			continue
		}
		state := lastTerminal([]journal.Record{last})
		if state == "" {
			state = "open"
		}
		presence, _ := Presence(workspace, id, now)
		glyph := "○"
		if style.Glyphs == "ascii" {
			glyph = "o"
		}
		if presence == "running" {
			glyph = "●"
			if style.Glyphs == "ascii" {
				glyph = "*"
			}
		} else if presence == "stale" {
			glyph += " " + labels["stale"]
		}
		items = append(items, deliveryItem{
			id: id, slug: opened.Slug, state: state, presence: presence, opened: first.At, updated: last.At,
			description: fmt.Sprintf("%s · %s · %s %s", renderer.status(state, false), glyph, pickerAge(now.Sub(last.At)), labels["ago"]),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		openI, openJ := items[i].state == "open", items[j].state == "open"
		if openI != openJ {
			return openI
		}
		return items[i].opened.After(items[j].opened)
	})
	result := make([]list.Item, len(items))
	for i := range items {
		result[i] = items[i]
	}
	return result, nil
}

func deliveryEndpoints(path string) (journal.Record, journal.Record, error) {
	handle, err := os.Open(path)
	if err != nil {
		return journal.Record{}, journal.Record{}, err
	}
	defer handle.Close()
	reader := bufio.NewReaderSize(io.LimitReader(handle, pickerLineLimit+1), 64<<10)
	firstLine, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return journal.Record{}, journal.Record{}, err
	}
	if len(firstLine) > pickerLineLimit {
		return journal.Record{}, journal.Record{}, journal.ErrInvalidRecord
	}
	lastLine, err := readLastJournalLine(handle)
	if err != nil {
		return journal.Record{}, journal.Record{}, err
	}
	var first, last journal.Record
	if json.Unmarshal(bytesTrimLine(firstLine), &first) != nil || json.Unmarshal(lastLine, &last) != nil {
		return journal.Record{}, journal.Record{}, journal.ErrInvalidRecord
	}
	return first, last, nil
}

func readLastJournalLine(handle *os.File) ([]byte, error) {
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, journal.ErrInvalidRecord
	}
	for window := int64(64 << 10); ; window *= 2 {
		if window > size {
			window = size
		}
		start := size - window
		data := make([]byte, window)
		if _, err := handle.ReadAt(data, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		trimmed := bytesTrimLine(data)
		if index := strings.LastIndexByte(string(trimmed), '\n'); index >= 0 {
			line := trimmed[index+1:]
			if len(line) > pickerLineLimit {
				return nil, journal.ErrInvalidRecord
			}
			return line, nil
		}
		if start == 0 && len(trimmed) > 0 {
			return trimmed, nil
		}
		if window >= pickerLineLimit {
			return nil, journal.ErrInvalidRecord
		}
		if window > pickerLineLimit/2 {
			window = pickerLineLimit / 2
		}
	}
}

func bytesTrimLine(value []byte) []byte {
	return []byte(strings.TrimRight(string(value), "\r\n"))
}

func pickerAge(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return fmt.Sprintf("%ds", int(age/time.Second))
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age/time.Minute))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age/time.Hour))
	}
	return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
}
