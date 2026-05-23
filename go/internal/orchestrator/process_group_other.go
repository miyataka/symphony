//go:build !unix

package orchestrator

import "os/exec"

func configureCommandProcessGroup(cmd *exec.Cmd) {
}
