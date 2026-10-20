//go:build !unix

package tfexec

import (
	"os"
	"os/exec"
)

// The console sends Ctrl-C to every process attached to it, Terraform
// included, so interragate never needs to pass one on.
func inForeground() bool { return true }

func leadsGroup() bool { return false }

func ownGroup(cmd *exec.Cmd) {}

// interrupt is only needed when a command is cancelled for some other
// reason than a signal; there is no gentler way to stop a process here.
func interrupt(p *os.Process, group bool) { p.Kill() }

func killGroup(cmd *exec.Cmd) error { return cmd.Process.Kill() }
