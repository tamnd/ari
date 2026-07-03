//go:build windows

package tool

import (
	"os/exec"
	"time"
)

// shellCommand builds the fresh shell for one call, cmd.exe on Windows.
func shellCommand(script string) *exec.Cmd {
	return exec.Command("cmd", "/c", script)
}

// withCwdSentinel is a no-op on Windows; cwd capture rides on POSIX
// shell syntax and Windows keeps the cwd it was given.
func withCwdSentinel(command string) string { return command }

// extractCwd finds no sentinel on Windows.
func extractCwd(output string) (cleaned, cwd string) { return output, "" }

// setpgid is a no-op on Windows; there is no process group to arm.
func setpgid(*exec.Cmd) {}

// killGroup kills the process itself; child cleanup on Windows would
// need a job object, which is deferred with the rest of the Windows
// port.
func killGroup(cmd *exec.Cmd, _ time.Duration) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
