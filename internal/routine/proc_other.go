//go:build !unix

package routine

import "os/exec"

func configureCommand(cmd *exec.Cmd) {}
