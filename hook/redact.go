package hook

import (
	"regexp"
	"strings"
)

// secretNamePattern flags environment variables whose value must never reach
// model context (D16). A hook that echoes such a value on stdout would leak
// it into the transcript through additionalContext, so the runner scrubs it
// before the output is parsed. The hook package stays stdlib-only, so this
// mirrors tool.redactSecrets rather than sharing it.
var secretNamePattern = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH)`)

// redactSecrets replaces the value of every secret-bearing environment
// variable that appears in a hook's stdout with a marker naming the variable,
// so an echoed key never lands in the transcript (doc 05 section 13, D16).
// The marker names the variable and never the value.
func redactSecrets(output string, environ []string) string {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || len(value) < 6 || !secretNamePattern.MatchString(name) {
			continue
		}
		output = strings.ReplaceAll(output, value, "<redacted:"+name+">")
	}
	return output
}
