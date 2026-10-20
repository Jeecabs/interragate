//go:build unix

package tfexec

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// inForeground reports whether interragate runs in the foreground of a
// terminal, which sends Ctrl-C to every process in interragate's process
// group.
func inForeground() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer tty.Close()
	pgrp, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
	return err == nil && pgrp == unix.Getpgrp()
}

// leadsGroup reports whether interragate leads its process group, as a
// command run from a shell does.
func leadsGroup() bool { return unix.Getpgrp() == os.Getpid() }

// ownGroup starts cmd in a process group of its own, out of reach of
// signals sent to interragate's.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt sends Terraform what Ctrl-C at a terminal would: SIGINT, which
// Terraform takes as a request to stop, and which its provider plugins
// ignore, leaving Terraform to stop them (SIGTERM would kill them in the
// middle of their work). A Terraform in a process group of its own gets it
// the way a terminal sends it, to the whole group, so that it also reaches
// a terraform that a wrapper script runs without passing signals on.
func interrupt(p *os.Process, group bool) {
	if group {
		syscall.Kill(-p.Pid, syscall.SIGINT)
		return
	}
	p.Signal(os.Interrupt)
}

// killGroup kills cmd, started with ownGroup, and everything it started.
func killGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
