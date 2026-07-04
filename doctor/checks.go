package doctor

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/hook"
	"github.com/tamnd/ari/journal"
	"github.com/tamnd/ari/mcp"
	"github.com/tamnd/ari/memory"
	"github.com/tamnd/ari/memory/sqlite"
)

// walWarnBytes is the size past which the colony's write-ahead log reads as
// unhealthy. SQLite auto-checkpoints the WAL at about four megabytes, so a WAL
// far past that means a checkpoint is starved, usually by a reader held open too
// long. It is a warning, not an error: the data is intact, but the file is
// growing where it should be folding back into the main database.
const walWarnBytes = 64 << 20

// checkNestPermissions verifies that the credential directory and the
// files inside it are not readable by other users on the box. The auth
// directory is where secret references live, so a world-readable file
// there leaks the colony's credentials to anyone with a shell (section
// 12.1, nest permissions). A missing auth directory is clean: there is
// nothing to leak yet.
func checkNestPermissions(ctx *Context) Finding {
	auth := ctx.Nest.AuthDir()
	info, err := os.Stat(auth)
	if os.IsNotExist(err) {
		return Finding{Status: StatusOK, Reason: "no credential directory yet, nothing to protect"}
	}
	if err != nil {
		return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not read %s: %v", auth, err)}
	}

	var loose []string
	if info.Mode().Perm()&0o077 != 0 {
		loose = append(loose, auth)
	}
	_ = filepath.WalkDir(auth, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == auth {
			return nil
		}
		fi, ferr := d.Info()
		if ferr != nil {
			return nil
		}
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm()&^want != 0 {
			loose = append(loose, path)
		}
		return nil
	})

	if len(loose) == 0 {
		return Finding{Status: StatusOK, Reason: "the credential directory is owner-only"}
	}
	return Finding{
		Status: StatusCritical,
		Reason: fmt.Sprintf("group or world can read credential paths: %s", strings.Join(loose, ", ")),
		Fix:    tightenNestPerms,
	}
}

// tightenNestPerms sets the auth directory to 0700 and every file under it
// to 0600. This is unambiguous and non-destructive, so --fix runs it
// (section 12.3).
func tightenNestPerms(ctx *Context) error {
	auth := ctx.Nest.AuthDir()
	if err := os.Chmod(auth, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(auth, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == auth {
			return err
		}
		mode := os.FileMode(0o600)
		if d.IsDir() {
			mode = 0o700
		}
		return os.Chmod(path, mode)
	})
}

// secretAssign matches a config assignment whose key names a credential.
// The value is captured so the check can tell an env reference from a
// literal without ever logging the value itself (D16).
var secretAssign = regexp.MustCompile(`(?i)^\s*(?:[\w.-]+\.)?(api[_-]?key|token|secret|password)\s*=\s*"([^"]*)"`)

// envReference is the ${NAME} form a config value should use instead of a
// literal, so the file is safe to commit or paste (section 6.4).
var envReference = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// checkSecretsInConfig scans every config file for a literal credential.
// A key in config is a key one cat away from the model and one git add
// away from a public repo, so it is a critical finding (section 6.4). The
// fix is a judgment call: ari will not invent an env name or a keychain
// entry for the operator, so it leaves manual guidance rather than
// papering over the choice.
func checkSecretsInConfig(ctx *Context) Finding {
	files := []string{
		ctx.Nest.GlobalConfig(),
		ctx.Nest.ProjectConfig(),
		ctx.Nest.LocalConfig(),
	}
	var hits []string
	for _, path := range files {
		names, err := literalSecretLines(path)
		if err != nil {
			return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not scan %s: %v", path, err)}
		}
		for _, key := range names {
			hits = append(hits, fmt.Sprintf("%s (%s)", key, filepath.Base(path)))
		}
	}
	if len(hits) == 0 {
		return Finding{Status: StatusOK, Reason: "config references credentials by env or keychain, never inline"}
	}
	return Finding{
		Status: StatusCritical,
		Reason: fmt.Sprintf("a literal credential is in config: %s", strings.Join(hits, ", ")),
		Manual: "Replace the literal value with a ${ENV_VAR} reference and set that variable in your environment or the OS keychain, then remove the literal.",
	}
}

// literalSecretLines returns the credential keys in a config file whose
// value is a non-empty literal rather than an env reference. It never
// returns the value.
func literalSecretLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var keys []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := secretAssign.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		value := m[2]
		if value == "" || envReference.MatchString(value) {
			continue
		}
		keys = append(keys, m[1])
	}
	return keys, sc.Err()
}

// checkConfigHealth reports a config that would not load or parse. A config
// ari cannot read is not a place to run an agent from, so a load error is
// critical, and the reason names the problem the loader already collected
// without echoing any secret it interpolated.
func checkConfigHealth(ctx *Context) Finding {
	if ctx.LoadErr != nil {
		return Finding{Status: StatusCritical, Reason: fmt.Sprintf("config does not load: %v", ctx.LoadErr)}
	}
	if ctx.Config == nil {
		return Finding{Status: StatusOK, Reason: "no config loaded"}
	}
	if w := ctx.Config.Warnings(); len(w) > 0 {
		return Finding{Status: StatusWarn, Reason: strings.Join(w, "; ")}
	}
	return Finding{Status: StatusOK, Reason: "config loads and validates"}
}

// checkPermissionMode flags a full-auto default. Full-auto plus untrusted
// content is the injection kill chain, so a persisted full-auto default is
// a warning that names the risk; whether it is acceptable for a given
// workspace is the operator's call, so there is no auto-fix (section 12.1,
// permission mode).
func checkPermissionMode(ctx *Context) Finding {
	if ctx.Config == nil {
		return Finding{Status: StatusOK, Reason: "no config loaded, permission mode defaults to ask"}
	}
	if ctx.Config.Mode == "full-auto" {
		return Finding{
			Status: StatusWarn,
			Reason: "the default permission mode is full-auto, which removes the review step every fetched or MCP-sourced instruction would otherwise pass",
			Manual: "Set the default mode back to ask or auto-edit, and pass --mode full-auto only for a run you mean to leave unattended.",
		}
	}
	return Finding{Status: StatusOK, Reason: fmt.Sprintf("permission mode defaults to %s", orAsk(ctx.Config.Mode))}
}

func orAsk(mode string) string {
	if mode == "" {
		return "ask"
	}
	return mode
}

// checkLocalGitignore warns when the per-user local config could be
// committed. local.toml holds a single operator's overrides and is meant
// to stay out of git; if the repo does not ignore it, a git add sweeps it
// into history. The fix is safe and unambiguous: append the ignore line.
func checkLocalGitignore(ctx *Context) Finding {
	root := ctx.Nest.Root
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return Finding{Status: StatusOK, Reason: "not a git repository, nothing to ignore"}
	}
	if _, err := os.Stat(ctx.Nest.ProjectDir()); os.IsNotExist(err) {
		return Finding{Status: StatusOK, Reason: "no project nest to ignore yet"}
	}
	if gitignoreCovers(root, ".ari/local.toml") {
		return Finding{Status: StatusOK, Reason: ".ari/local.toml is gitignored"}
	}
	return Finding{
		Status: StatusWarn,
		Reason: ".ari/local.toml is not gitignored, so a git add could commit your per-user overrides",
		Fix:    appendLocalIgnore,
	}
}

// gitignoreCovers reports whether the repo's top-level .gitignore has a
// line that ignores target. It matches the exact path, the .ari directory,
// or a bare .ari entry, which are the three ways an operator writes it.
func gitignoreCovers(root, target string) bool {
	f, err := os.Open(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch strings.TrimSuffix(line, "/") {
		case target, ".ari", filepath.Dir(target):
			return true
		}
	}
	return false
}

// appendLocalIgnore adds the ignore line to the repo's .gitignore, keeping
// any existing content. It creates the file if there is none.
func appendLocalIgnore(ctx *Context) error {
	path := filepath.Join(ctx.Nest.Root, ".gitignore")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	prefix := ""
	if info, serr := f.Stat(); serr == nil && info.Size() > 0 {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + ".ari/local.toml\n")
	return err
}

// checkBindStatus reports the listening surface. M0 has only the serve
// stub and persists no listener config, so there is nothing bound beyond
// loopback to flag. The check ships now, reporting the empty surface,
// because M5 brings a real serve mode into a doctor that already audits it
// rather than one bolted on after the port is live (section 12.1).
func checkBindStatus(ctx *Context) Finding {
	return Finding{Status: StatusOK, Reason: "no serve listener is configured; nothing is bound beyond loopback"}
}

// checkWorkspaceTrust reports the hook trust gate: whether this workspace is
// trusted and which of its configured hooks would run. A repo can carry hooks
// in its committed config, and those never run until the operator explicitly
// trusts the workspace, so an untrusted workspace with repo hooks is a warning
// that names them rather than a silent no-op (doc 05 section 12, D16). User
// hooks always run because the operator wrote them, so they are reported as
// active regardless of trust.
func checkWorkspaceTrust(ctx *Context) Finding {
	if ctx.Config == nil {
		return Finding{Status: StatusOK, Reason: "no config loaded, no hooks to gate"}
	}
	var user, repo []hook.Command
	for _, c := range ctx.Config.Hooks() {
		if c.Layer == "user" {
			user = append(user, c)
		} else {
			repo = append(repo, c)
		}
	}
	if len(user) == 0 && len(repo) == 0 {
		return Finding{Status: StatusOK, Reason: "no hooks configured"}
	}
	trusted := hook.LoadTrust(ctx.Nest.TrustFile()).IsTrusted(ctx.Nest.Root)
	if len(repo) == 0 {
		return Finding{Status: StatusOK, Reason: fmt.Sprintf("%d user hook(s) active; no repo hooks to gate", len(user))}
	}
	if trusted {
		return Finding{Status: StatusOK, Reason: fmt.Sprintf("workspace is trusted; %d user and %d repo hook(s) active", len(user), len(repo))}
	}
	return Finding{
		Status: StatusWarn,
		Reason: fmt.Sprintf("this workspace is untrusted, so %d repo hook(s) will not run: %s", len(repo), describeHooks(repo)),
		Manual: "Review the hooks above, then run `ari trust` in this workspace to let its repo hooks run. Leave it untrusted if you did not write them.",
	}
}

// describeHooks renders a short, comma-joined summary of the repo hooks a
// doctor finding names, so the operator sees what trusting the workspace would
// let run before deciding.
func describeHooks(cmds []hook.Command) string {
	lines := make([]string, 0, len(cmds))
	for _, c := range cmds {
		lines = append(lines, hook.Describe(c))
	}
	return strings.Join(lines, "; ")
}

// checkProjectMemorySize warns when ARI.md is larger than the per-file cap
// the memory loader trims to. An oversized ARI.md is not an error, because
// the loader caps it and the session still runs, but a file the ant only
// partly reads is a house rule the operator thinks is in force and is not,
// so the check names the size and the cap and leaves the trim to the author
// (doc 01 section 7, D21).
func checkProjectMemorySize(ctx *Context) Finding {
	path := ctx.Nest.ARIMD()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Finding{Status: StatusOK, Reason: "no ARI.md, nothing to size"}
	}
	if err != nil {
		return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not read %s: %v", path, err)}
	}
	if info.Size() > int64(memory.DefaultPerFileCap) {
		return Finding{
			Status: StatusWarn,
			Reason: fmt.Sprintf("ARI.md is %d bytes, over the %d-byte cap, so the ant only reads the first part of it", info.Size(), memory.DefaultPerFileCap),
			Manual: "Trim ARI.md under the cap, or move the detail into a skill the ant loads on demand, so every house rule in it is actually in force.",
		}
	}
	return Finding{Status: StatusOK, Reason: fmt.Sprintf("ARI.md is %d bytes, under the %d-byte cap", info.Size(), memory.DefaultPerFileCap)}
}

// checkColonyMemory audits the memory substrate M2 added: the colony database.
// It is the D16 posture applied to the new store. It confirms the database has
// not leaked into a committable path, that it is at the head schema version this
// build knows how to run, and that its write-ahead log is not growing unchecked.
// A fresh install with no colony.db yet is clean; memory writes the file on the
// first run.
func checkColonyMemory(ctx *Context) Finding {
	// The colony database lives in the global state directory, outside the repo.
	// A colony.db sitting inside the workspace would be committed, and with it
	// every remembered fact, so a stray one in a committable path is a leak.
	for _, p := range []string{
		filepath.Join(ctx.Nest.Root, "colony.db"),
		filepath.Join(ctx.Nest.ProjectDir(), "colony.db"),
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return Finding{
				Status: StatusCritical,
				Reason: fmt.Sprintf("a colony.db is inside the workspace at %s, so the colony's memory would be committed", p),
				Manual: "Delete the in-repo colony.db and add it to .gitignore. The real database lives under the global state directory, not the checkout.",
			}
		}
	}

	path := ctx.Nest.ColonyDB()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Finding{Status: StatusOK, Reason: "no colony.db yet; memory initializes on the first run"}
	}
	if err != nil {
		return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not read %s: %v", path, err)}
	}
	if info.IsDir() {
		return Finding{Status: StatusCritical, Reason: fmt.Sprintf("%s is a directory, not the colony database", path)}
	}

	store, err := sqlite.Open(path)
	if err != nil {
		return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not open the colony database: %v", err)}
	}
	defer func() { _ = store.Close() }()

	head, err := sqlite.HeadVersion()
	if err != nil {
		return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not read the head schema version: %v", err)}
	}
	ver, err := store.SchemaVersion(context.Background())
	if err != nil {
		return Finding{Status: StatusWarn, Reason: fmt.Sprintf("could not read the colony schema version: %v", err)}
	}
	switch {
	case ver > head:
		return Finding{
			Status: StatusCritical,
			Reason: fmt.Sprintf("colony.db is at schema %d, past this build's head %d; a newer ari wrote it", ver, head),
			Manual: "Upgrade ari to a build that knows schema " + fmt.Sprint(ver) + " before running against this colony, or the migration walk will not recognize it.",
		}
	case ver < head:
		return Finding{
			Status: StatusWarn,
			Reason: fmt.Sprintf("colony.db is at schema %d, behind head %d; it migrates forward on the next run", ver, head),
		}
	}

	if wi, err := os.Stat(path + "-wal"); err == nil && wi.Size() > walWarnBytes {
		return Finding{
			Status: StatusWarn,
			Reason: fmt.Sprintf("the colony write-ahead log is %d bytes, past the %d-byte checkpoint threshold, so a checkpoint is likely starved", wi.Size(), walWarnBytes),
			Manual: "Close any stale ari session holding a read open, then run once to let the WAL checkpoint fold back into colony.db.",
		}
	}

	return Finding{Status: StatusOK, Reason: fmt.Sprintf("colony.db is at head schema %d with a healthy write-ahead log", head)}
}

// checkLanguageServer reports the LSP surface: whether the client is enabled
// and, when it is, whether gopls is on the PATH. LSP is off by default, so a
// disabled client is clean and the reason names the opt-in. An enabled client
// with no gopls is a warning, not an error, because a missing server degrades
// to zero diagnostics rather than a failed edit (doc 13, plan 02 slice 5), but
// the operator asked for diagnostics and is not getting them, so it is worth a
// line.
func checkLanguageServer(ctx *Context) Finding {
	if ctx.Config == nil || !ctx.Config.LSP.Enabled {
		return Finding{Status: StatusOK, Reason: "LSP is off; enable it in config to get diagnostics folded into edit results"}
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		return Finding{
			Status: StatusWarn,
			Reason: "LSP is enabled but gopls is not on the PATH, so Go edits get no diagnostics",
			Manual: "Install gopls (go install golang.org/x/tools/gopls@latest) and make sure it is on the PATH, or turn LSP off if you do not want it.",
		}
	}
	return Finding{Status: StatusOK, Reason: "LSP is enabled and gopls is on the PATH"}
}

// checkMCPServers lists the MCP servers the config chain declares, so the
// operator can see the tool surface a session would attach at a glance. A
// server is untrusted content by construction (D20), so naming them here is
// the audit companion to the workspace-trust check that gates hooks: together
// they show the two outside surfaces this milestone added. A malformed mcp.toml
// is a warning that names the file rather than a silent drop.
func checkMCPServers(ctx *Context) Finding {
	cfg, err := mcp.Discover(mcp.Options{
		Root:      ctx.Nest.Root,
		Cwd:       ctx.Nest.Root,
		GlobalDir: ctx.Nest.Global,
	})
	if err != nil {
		return Finding{
			Status: StatusWarn,
			Reason: fmt.Sprintf("an mcp.toml did not parse, so its servers will not load: %v", err),
			Manual: "Fix the toml syntax the reason names, then rerun doctor.",
		}
	}
	if len(cfg.Servers) == 0 {
		return Finding{Status: StatusOK, Reason: "no MCP servers configured"}
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return Finding{Status: StatusOK, Reason: fmt.Sprintf("%d MCP server(s) configured: %s", len(names), strings.Join(names, ", "))}
}

// checkJournalContinuity verifies the event log is readable and its
// sequence numbers run gap-free from one, which is the tamper-evidence M0
// can honestly offer before the section 11 hash chain lands: a deleted or
// reordered entry shows up as a gap or a backward step. --audit runs the
// same check today and gains the chain verification when it exists.
func checkJournalContinuity(ctx *Context) Finding {
	dir := ctx.Nest.JournalDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return Finding{Status: StatusOK, Reason: "no journal yet, nothing to verify"}
	}
	j, err := journal.Open(dir, nil)
	if err != nil {
		return Finding{Status: StatusCritical, Reason: fmt.Sprintf("cannot open the journal: %v", err)}
	}
	events, err := j.Since(context.Background(), 0)
	if err != nil {
		return Finding{Status: StatusCritical, Reason: fmt.Sprintf("journal is unreadable, history may be corrupt: %v", err)}
	}
	if broken := firstGap(events); broken != "" {
		return Finding{Status: StatusCritical, Reason: broken}
	}
	return Finding{Status: StatusOK, Reason: fmt.Sprintf("journal is continuous across %d events", len(events))}
}

// firstGap returns a description of the first sequence break, or the empty
// string when the events run 1, 2, 3, ... with no gap and no repeat.
func firstGap(events []event.Event) string {
	var want uint64 = 1
	for _, e := range events {
		if e.Seq != want {
			return fmt.Sprintf("journal sequence breaks at %d (expected %d), so an entry was edited, deleted, or reordered", e.Seq, want)
		}
		want++
	}
	return ""
}
