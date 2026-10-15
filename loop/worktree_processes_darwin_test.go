package loop

import "golang.org/x/sys/unix"

// SZOMB is 5 in Darwin's sys/proc.h; x/sys/unix does not export it.
const darwinZombieState = 5

// processIsZombie reports a zombie, or a process sysctl cannot find, as ended.
func processIsZombie(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err != nil || info == nil || info.Proc.P_stat == darwinZombieState
}
