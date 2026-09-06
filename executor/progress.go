package executor

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var progressLine = regexp.MustCompile(`^BATUTA-PROGRESS ([0-9]+) (START|DONE)$`)

// ParseProgress parses one complete executor progress protocol line.
func ParseProgress(line string) (criterion int, state string, ok bool) {
	match := progressLine.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return 0, "", false
	}
	criterion, err := strconv.Atoi(match[1])
	if err != nil || criterion < 1 {
		return 0, "", false
	}
	return criterion, match[2], true
}

// ProgressEvent reports one acceptance criterion transition from an executor.
type ProgressEvent struct {
	Criterion int
	State     string
	At        time.Time
}

type progressObserver struct {
	pending []byte
	sink    *progressSink
}

type progressSink struct {
	mu       sync.Mutex
	events   []ProgressEvent
	callback func(ProgressEvent)
}

func (o *progressObserver) Write(payload []byte) (int, error) {
	written := len(payload)
	o.pending = append(o.pending, payload...)
	for {
		newline := bytes.IndexByte(o.pending, '\n')
		if newline < 0 {
			break
		}
		o.parse(string(o.pending[:newline]))
		o.pending = o.pending[newline+1:]
	}
	return written, nil
}

func (o *progressObserver) flush() {
	if len(o.pending) == 0 {
		return
	}
	o.parse(string(o.pending))
	o.pending = nil
}

func (o *progressObserver) parse(line string) {
	criterion, state, ok := ParseProgress(line)
	if !ok {
		return
	}
	o.sink.emit(criterion, state)
}

func (s *progressSink) emit(criterion int, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	event := ProgressEvent{Criterion: criterion, State: state, At: time.Now()}
	s.events = append(s.events, event)
	if s.callback != nil {
		s.callback(event)
	}
}
