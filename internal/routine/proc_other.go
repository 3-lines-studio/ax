//go:build !unix

package routine

import "os/exec"

// configureCommand is a no-op outside Unix: exec kills the entry process when
// the context ends, and process groups are not portable.
func configureCommand(cmd *exec.Cmd) {}
