//go:build !windows

package acp

import (
	"context"
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
}

type ownedProcesses struct {
	identities map[int]processIdentity
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
		entries, err := processSnapshot()
		if err != nil {
			return // Discovery failure cannot change the unresolved cleanup verdict.
		}
		if len(o.identities) == 0 {
			for _, entry := range entries {
				if entry.identity.pid == pid {
					o.identities[pid] = entry.identity
					break
				}
			}
			if len(o.identities) == 0 {
				return
			}
		}
		o.observe(entries)
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
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
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,lstart=")
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
		if len(fields) != 7 {
			return nil, ErrCleanupUnresolved
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil || pid <= 0 || parent < 0 {
			return nil, ErrCleanupUnresolved
		}
		entries = append(entries, processEntry{identity: processIdentity{pid: pid, started: strings.Join(fields[2:], " ")}, parent: parent})
	}
	return entries, nil
}
