package loop

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// processIsZombie reports a zombie, or a process whose state cannot be read, as ended.
func processIsZombie(pid int) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	state := strings.TrimSpace(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return state == "" || state[0] == 'Z'
}
