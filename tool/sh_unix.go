//go:build unix

package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// posixShells are the shells the cwd sentinel's syntax is known to fit.
// A user shell outside this set (fish, csh) would misparse the trailer,
// so those sessions run /bin/sh instead.
var posixShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

// shellCommand builds the fresh shell for one call. The user's shell
// when set and POSIX, /bin/sh otherwise; never a long-lived process
// (doc 04 section 7.2).
func shellCommand(script string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" || !posixShells[filepath.Base(shell)] {
		shell = "/bin/sh"
	}
	return exec.Command(shell, "-c", script)
}

// cwdSentinel brackets the captured $PWD so it never collides with real
// output; NUL does not appear in text a command prints. The script side
// emits it via printf octal escapes because an argv string cannot carry
// a literal NUL.
const cwdSentinel = "\x00ARI_CWD:"

// withCwdSentinel appends a trailer that reports the shell's final
// working directory while preserving the command's exit code, which is
// how a bare cd in one call carries to the next without a live shell
// (doc 04 section 7.2).
func withCwdSentinel(command string) string {
	return command + "\n__ari_exit=$?\nprintf '\\000ARI_CWD:%s\\000' \"$PWD\"\nexit $__ari_exit"
}

// extractCwd strips the sentinel from the output and returns the final
// cwd the shell reported, empty when the shell never reached the
// trailer (a kill or a hard exit).
func extractCwd(output string) (cleaned, cwd string) {
	start := strings.LastIndex(output, cwdSentinel)
	if start < 0 {
		return output, ""
	}
	rest := output[start+len(cwdSentinel):]
	dir, tail, found := strings.Cut(rest, "\x00")
	if !found {
		return output, ""
	}
	return output[:start] + tail, dir
}

// setpgid puts the command in its own process group so a kill reaches
// the whole tree, not just the shell (doc 04 section 7.3).
func setpgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup terminates the command's process group: SIGTERM first, then
// SIGKILL after the grace period for anything that ignored it.
func killGroup(cmd *exec.Cmd, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	time.Sleep(grace)
	_ = syscall.Kill(pgid, syscall.SIGKILL)
}
