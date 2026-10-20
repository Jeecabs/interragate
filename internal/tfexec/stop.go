package tfexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Stopped is the cause of a context that ended because interragate received
// a signal asking it to stop: Ctrl-C, a termination or a hangup.
type Stopped struct{ Signal os.Signal }

func (s *Stopped) Error() string {
	if s.Signal == os.Interrupt {
		return "interrupted"
	}
	return "stopped (" + s.Signal.String() + ")"
}

// ExitCode is the status a shell gives a command killed by the signal.
func (s *Stopped) ExitCode() int {
	if n, ok := s.Signal.(syscall.Signal); ok {
		return 128 + int(n)
	}
	return 1
}

// runCommand starts cmd and waits for it. If ctx ends first, Terraform is
// asked to stop (see stop), and interragate waits for it to finish stopping:
// that can take a while, and it is what saves the state and releases the
// lock.
func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	shared := inForeground()
	if !shared {
		ownGroup(cmd)
	}
	// Terraform stops its providers when it exits. Anything else it started
	// that still holds its output open must not keep interragate waiting.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stop(cmd.Process, shared, context.Cause(ctx))
		case <-exited:
		}
	}()
	err := cmd.Wait()
	close(exited)
	if errors.Is(err, exec.ErrWaitDelay) {
		// It succeeded; something it left behind held the output open.
		err = nil
	}
	return err
}

// stop passes interragate's stop on to a running Terraform, once, as the
// Ctrl-C a terminal would send (see interrupt).
//
// Terraform takes Ctrl-C as a request to stop: it finishes or cancels what
// is in flight, saves the state and releases the lock. A second one makes
// it exit at once, which can leave the lock behind and lose state, so it
// must hear about one stop exactly once.
//
// In the foreground of a terminal, Terraform shares interragate's process
// group, so that the terminal's Ctrl-C reaches it directly and it can still
// prompt, for a passphrase say. interragate then passes on only a
// termination or hangup sent to interragate alone, which is what reaches a
// command run from a shell, the leader of its group. When interragate does
// not lead its group, as under timeout(1) or make, the sender signalled the
// whole group, Terraform included. (A hangup that does reach Terraform kills
// it outright; there is nothing to do about that.) Anywhere else, Terraform
// runs in a process group of its own, out of reach of signals for
// interragate's, and interragate passes every stop on.
func stop(p *os.Process, shared bool, cause error) {
	var st *Stopped
	if errors.As(cause, &st) && !passOn(st.Signal, shared, leadsGroup()) {
		return
	}
	interrupt(p, !shared)
}

// passOn reports whether Terraform needs to hear about interragate receiving
// sig, rather than having received it too.
func passOn(sig os.Signal, shared, leader bool) bool {
	return !shared || (sig != os.Interrupt && leader)
}
