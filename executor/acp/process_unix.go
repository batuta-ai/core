//go:build !windows

package acp

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxOwnedProcesses = 256

type processIdentity struct {
	pid     int
	started string
}

type processEntry struct {
	identity processIdentity
	parent   int
	group    int
}

type ownedProcesses struct {
	identities map[int]processIdentity
	uncertain  bool
}

func processAvailable() error { return nil }

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// The process table is advisory discovery, never authority to signal a PID.
// Start timestamps can be coarse, and lookup followed by kill has a reuse race.
func (o *ownedProcesses) observe(entries []processEntry) {
	for changed := true; changed && len(o.identities) < maxOwnedProcesses; {
		changed = false
		current := make(map[int]processIdentity, len(entries))
		for _, entry := range entries {
			current[entry.identity.pid] = entry.identity
		}
		for _, entry := range entries {
			parent, found := o.identities[entry.parent]
			if !found || current[entry.parent] != parent {
				continue
			}
			if _, exists := o.identities[entry.identity.pid]; !exists {
				o.identities[entry.identity.pid] = entry.identity
				changed = true
				if len(o.identities) == maxOwnedProcesses {
					o.uncertain = true
					return
				}
			}
		}
	}
}

func (o *ownedProcesses) track(pid int, stop <-chan struct{}) {
	o.identities = make(map[int]processIdentity)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		default:
		}
		o.snapshot(pid)
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func (o *ownedProcesses) snapshot(group int) []processEntry {
	entries, err := processSnapshot()
	if err != nil {
		o.uncertain = true
		return nil
	}
	// Group members remain discoverable even after the root exits and children
	// are reparented. These identities are evidence only, never signal targets.
	for _, entry := range entries {
		if entry.group == group {
			if _, exists := o.identities[entry.identity.pid]; !exists {
				if len(o.identities) == maxOwnedProcesses {
					o.uncertain = true
					break
				}
				o.identities[entry.identity.pid] = entry.identity
				if len(o.identities) == maxOwnedProcesses {
					o.uncertain = true
				}
			}
		}
	}
	o.observe(entries)
	return entries
}

func (o *ownedProcesses) resolved(group int, entries []processEntry) bool {
	if o.uncertain {
		return false
	}
	for _, entry := range entries {
		if identity, found := o.identities[entry.identity.pid]; found && identity == entry.identity && entry.group != group {
			return false
		}
	}
	return true
}

func (p *Process) shutdownGroup() error {
	group := p.cmd.Process.Pid
	// Capture observable escapes while the root's parent relationships still
	// exist; closing stdin or signaling the group can cause reparenting.
	p.owned.snapshot(group)
	p.Connection.Close()
	for _, stage := range []struct {
		signal syscall.Signal
		grace  time.Duration
	}{
		{grace: 100 * time.Millisecond},
		{signal: syscall.SIGTERM, grace: 100 * time.Millisecond},
		{signal: syscall.SIGKILL, grace: time.Second},
	} {
		absent := false
		if stage.signal != 0 {
			err := syscall.Kill(-group, stage.signal)
			absent = errors.Is(err, syscall.ESRCH)
			if err != nil && !absent {
				return ErrCleanupUnresolved
			}
		}
		var drained bool
		var err error
		if absent {
			drained, err = waitProcessReaped(p.exited)
		} else {
			drained, err = waitProcessGroup(group, p.exited, stage.grace)
		}
		if err != nil {
			return ErrCleanupUnresolved
		}
		if drained {
			if !p.owned.resolved(group, p.owned.snapshot(group)) {
				return ErrCleanupUnresolved
			}
			return nil
		}
	}
	return ErrCleanupUnresolved
}

func waitProcessGroup(group int, exited <-chan struct{}, grace time.Duration) (bool, error) {
	return waitProcessGroupStatus(func() error { return syscall.Kill(-group, 0) }, exited, grace)
}

func waitProcessGroupStatus(probe func() error, exited <-chan struct{}, grace time.Duration) (bool, error) {
	limit := time.NewTimer(grace)
	defer limit.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := probe()
		if errors.Is(err, syscall.ESRCH) {
			// Once disappearance is observed, never probe or signal this group ID again.
			return waitProcessReaped(exited)
		}
		// EPERM cannot establish absence; keep polling and allow escalation
		// when this stage expires, just as for a group that is still present.
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return false, err
		}
		select {
		case <-limit.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func waitProcessReaped(exited <-chan struct{}) (bool, error) {
	// Completed reaping wins even when the preceding stage has expired.
	select {
	case <-exited:
		return true, nil
	default:
	}
	// Group absence is not proof of direct-child reaping. Give Wait its own
	// bounded budget, independent of the grace period for group disappearance.
	limit := time.NewTimer(time.Second)
	defer limit.Stop()
	select {
	case <-exited:
		return true, nil
	case <-limit.C:
		return false, ErrCleanupUnresolved
	}
}

type processTable struct{ data []byte }

func (b *processTable) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-len(b.data) {
		return 0, ErrCapacity
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func processSnapshot() ([]processEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,pgid=,lstart=")
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	cmd.WaitDelay = 100 * time.Millisecond
	var table processTable
	cmd.Stdout = &table
	if err := cmd.Run(); err != nil {
		return nil, ErrCleanupUnresolved
	}
	var entries []processEntry
	for _, line := range strings.Split(string(table.data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 8 {
			return nil, ErrCleanupUnresolved
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		group, groupErr := strconv.Atoi(fields[2])
		if pidErr != nil || parentErr != nil || groupErr != nil || pid <= 0 || parent < 0 || group < 0 {
			return nil, ErrCleanupUnresolved
		}
		entries = append(entries, processEntry{identity: processIdentity{pid: pid, started: strings.Join(fields[3:], " ")}, parent: parent, group: group})
	}
	return entries, nil
}
