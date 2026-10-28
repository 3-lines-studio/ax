//go:build unix

package routine

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureCommand puts the routine in its own process group and makes a
// timeout or a cancelled run kill the whole group. Without it, a run that
// spawns children leaves them behind when the entry script is killed.
func configureCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// The whole group is already gone; nothing to clean up.
			return nil
		}
		return err
	}
}
