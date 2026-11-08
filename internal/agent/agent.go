package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brickhouse-tech/sync-agents/internal/agent/templates"
	"github.com/brickhouse-tech/sync-agents/internal/version"
)

var AllTargets = []string{"claude", "windsurf", "cursor", "copilot"}

// App is the per-invocation state container for sync-agents commands.
// One App is constructed by main(), populated from CLI flags in
// PersistentPreRunE, then handed to each command's library function.
//
// Fields are intentionally a flat set of values — no embedded
// configuration objects — so that test setup is a one-shot literal
// (e.g., &App{ProjectRoot: t.TempDir(), GlobalRoot: t.TempDir() + "/.agents"})
// without builders or option-functions.
type App struct {
	// ProjectRoot is the absolute path of the project's working tree
	// — the directory containing `.agents/`. Resolved by
	// FindProjectRoot at startup, or overridden by the --dir flag.
	ProjectRoot string

	// GlobalRoot is the absolute path of the user's `.agents/` tree at
	// user scope, when overridden programmatically. Empty means
	// "consult $SYNC_AGENTS_GLOBAL_ROOT or fall back to $HOME/.agents"
	// — see ResolveGlobalRoot in globalroot.go for the full precedence
	// chain, and SPEC-002 §Configurable global root for the
	// requirement.
	//
	// Tests set this directly to a t.TempDir-backed path so they never
	// touch the real $HOME. The CLI populates it from --global-root.
	GlobalRoot string

	// DryRun, when true, prints the operations that would be performed
	// but does not modify the filesystem.
	DryRun bool

	// Force, when true, allows commands to overwrite existing files or
	// symlinks that would otherwise be left alone.
	Force bool

	// ActiveTargets is the list of tool IDs the current command should
	// touch. Populated from .agents/config and overridden by the
	// --targets flag.
	ActiveTargets []string

	// Stdout and Stderr are the writers used by Info/Warn/Error. Tests
	// inject bytes.Buffer here to assert on output without capturing
	// the real stdio.
	Stdout io.Writer
	Stderr io.Writer
}

func NewApp() *App {
	return &App{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

func (a *App) Info(msg string)  { fmt.Fprintf(a.Stdout, "[info] %s\n", msg) }
func (a *App) Warn(msg string)  { fmt.Fprintf(a.Stdout, "[warn] %s\n", msg) }
func (a *App) Error(msg string) { fmt.Fprintf(a.Stderr, "[error] %s\n", msg) }

func FindProjectRoot(startDir string) string {
	if startDir == "" {
		startDir = "."
	}
	dir, err := filepath.Abs(startDir)
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".agents")); err == nil && fi.IsDir() {
			return dir
		}
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	wd, _ := os.Getwd()
	return wd
}

func (a *App) EnsureAgentsDir() error {
	path := filepath.Join(a.ProjectRoot, ".agents")
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		a.Error(".agents/ directory not found. Run 'sync-agents init' first.")
		return fmt.Errorf("no agents dir")
	}
	return nil
}

func ResolveTargetDir(target, root string) string {
	if target == "copilot" {
		return filepath.Join(root, ".github", "copilot")
	}
	return filepath.Join(root, "."+target)
}

func ResolveAgentsRel(target string) string {
	if target == "copilot" {
		return "../../.agents"
	}
	return "../.agents"
}

func ReadConfigTargets(projectRoot string) []string {
	configFile := filepath.Join(projectRoot, ".agents", "config")
	data, err := os.ReadFile(configFile)
	if err != nil {
		return copyTargets(AllTargets)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		if key == "targets" {
			val := strings.TrimSpace(parts[1])
			var result []string
			for _, t := range strings.Split(val, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					result = append(result, t)
				}
			}
			if len(result) > 0 {
				return result
			}
		}
	}
	return copyTargets(AllTargets)
}

func copyTargets(t []string) []string {
	r := make([]string, len(t))
	copy(r, t)
	return r
}

func (a *App) CreateSymlink(source, target string, dryRun bool) error {
	if dryRun {
		fmt.Fprintf(a.Stdout, "  would link: %s -> %s\n", target, source)
		return nil
	}

	os.MkdirAll(filepath.Dir(target), 0755)

	fi, err := os.Lstat(target)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			existing, _ := os.Readlink(target)
			if existing == source {
				return nil
			}
			if a.Force {
				os.Remove(target)
			} else {
				a.Warn(fmt.Sprintf("Symlink already exists: %s -> %s (use --force to overwrite)", target, existing))
				return fmt.Errorf("exists")
			}
		} else {
			if a.Force {
				os.RemoveAll(target)
			} else {
				a.Warn(fmt.Sprintf("File already exists: %s (use --force to overwrite)", target))
				return fmt.Errorf("exists")
			}
		}
	}

	if err := os.Symlink(source, target); err != nil {
		return err
	}
	a.Info(fmt.Sprintf("Linked: %s -> %s", target, source))
	return nil
}

func (a *App) PrintTree(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	count := len(names)
	for i, name := range names {
		connector := "├── "
		childPrefix := "│   "
		if i == count-1 {
			connector = "└── "
			childPrefix = "    "
		}
		fullPath := filepath.Join(dir, name)
		fi, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			linkTarget, _ := os.Readlink(fullPath)
			fmt.Fprintf(a.Stdout, "%s%s%s -> %s\n", prefix, connector, name, linkTarget)
		} else if fi.IsDir() {
			fmt.Fprintf(a.Stdout, "%s%s%s/\n", prefix, connector, name)
			a.PrintTree(fullPath, prefix+childPrefix)
		} else {
			fmt.Fprintf(a.Stdout, "%s%s%s\n", prefix, connector, name)
		}
	}
}

// -------------------------------------------------------------------------
// Commands
// -------------------------------------------------------------------------

func (a *App) CmdInit() error {
	a.Info("Initializing .agents/ directory structure...")

	agentsDir := filepath.Join(a.ProjectRoot, ".agents")
	for _, sub := range InitBucketDirs() {
		os.MkdirAll(filepath.Join(agentsDir, sub), 0755)
	}

	stateRule := filepath.Join(agentsDir, "rules", "state.md")
	if _, err := os.Stat(stateRule); os.IsNotExist(err) {
		os.WriteFile(stateRule, []byte(templates.State()), 0644)
		a.Info("Created .agents/rules/state.md from template")
	} else {
		a.Warn(".agents/rules/state.md already exists, skipping")
	}

	// Migrate legacy STATE.md
	legacyState := filepath.Join(agentsDir, "STATE.md")
	if _, err := os.Stat(legacyState); err == nil {
		a.migrateLegacyState(agentsDir)
	}

	// Config
	configFile := filepath.Join(agentsDir, "config")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		content := "# sync-agents configuration\n# Comma-separated list of sync targets (available: claude, windsurf, cursor, copilot)\n# Override per-command with: sync-agents sync --targets claude,cursor\ntargets = claude,windsurf,cursor,copilot\n"
		os.WriteFile(configFile, []byte(content), 0644)
		a.Info("Created .agents/config")
	} else {
		a.Warn(".agents/config already exists, skipping")
	}

	// AGENTS.md
	agentsMD := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); os.IsNotExist(err) {
		if err := a.generateAgentsMD(); err != nil {
			return err
		}
		a.Info("Created AGENTS.md")
	} else {
		a.Warn("AGENTS.md already exists, skipping (run 'sync-agents index' to regenerate)")
	}

	a.addDefaultGitignoreEntries()

	a.Info("Initialization complete. Directory structure:")
	a.PrintTree(agentsDir, "")
	return nil
}

func (a *App) CmdAdd(typ, name string) error {
	if typ == "" || name == "" {
		a.Error(fmt.Sprintf("Usage: sync-agents add <%s> <name>", strings.Join(ArtifactNames(), "|")))
		return fmt.Errorf("missing args")
	}

	bucket, ok := BucketForTypeString(typ)
	if !ok || bucket.NewTemplate == nil {
		a.Error(fmt.Sprintf("Unknown type: %s. Must be one of: %s", typ, strings.Join(ArtifactNames(), ", ")))
		return fmt.Errorf("unknown type")
	}
	typ = bucket.Dir

	if err := a.EnsureAgentsDir(); err != nil {
		return err
	}

	var fpath string
	if bucket.DirPerArtifact {
		fpath = filepath.Join(a.ProjectRoot, ".agents", typ, name, "SKILL.md")
	} else {
		fpath = filepath.Join(a.ProjectRoot, ".agents", typ, name+".md")
	}

	if _, err := os.Stat(fpath); err == nil && !a.Force {
		a.Error(fmt.Sprintf("File already exists: %s (use --force to overwrite)", fpath))
		return fmt.Errorf("exists")
	}

	content := strings.ReplaceAll(bucket.NewTemplate(), "${NAME}", name)

	os.MkdirAll(filepath.Dir(fpath), 0755)
	os.WriteFile(fpath, []byte(content), 0644)
	a.Info(fmt.Sprintf("Created %s: %s", typ, fpath))

	if err := a.generateAgentsMD(); err != nil {
		return err
	}
	a.Info("Updated AGENTS.md index")
	return nil
}

func (a *App) CmdSync() error {
	if err := a.EnsureAgentsDir(); err != nil {
		return err
	}

	agentsAbs, _ := filepath.Abs(filepath.Join(a.ProjectRoot, ".agents"))
	_ = agentsAbs

	a.Info("Syncing .agents/ to agent directories...")

	for _, target := range a.ActiveTargets {
		targetDir := ResolveTargetDir(target, a.ProjectRoot)
		agentsRel := ResolveAgentsRel(target)

		relDisplay := targetDir
		if strings.HasPrefix(targetDir, a.ProjectRoot+"/") {
			relDisplay = targetDir[len(a.ProjectRoot)+1:]
		}
		a.Info(fmt.Sprintf("Syncing to %s/", relDisplay))

		for _, b := range Buckets {
			if !b.SyncsToLocalTarget(target) {
				continue
			}
			subdirPath := filepath.Join(a.ProjectRoot, ".agents", b.Dir)
			if fi, err := os.Stat(subdirPath); err == nil && fi.IsDir() {
				sourceRel := agentsRel + "/" + b.Dir
				a.CreateSymlink(sourceRel, filepath.Join(targetDir, b.Dir), a.DryRun)
			}
		}
	}

	// CLAUDE.md -> AGENTS.md
	agentsMD := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); err == nil {
		a.CreateSymlink("AGENTS.md", filepath.Join(a.ProjectRoot, "CLAUDE.md"), a.DryRun)
	}

	// Hooks (SPEC-004 Part C): merge .agents/hooks/*.json fragments
	// into .claude/settings.json. This runs after symlink creation
	// because hooks are a JSON merge, not a directory symlink.
	if a.isBucketActive("claude") {
		hooksDir := filepath.Join(a.ProjectRoot, ".agents", "hooks")
		settingsPath := filepath.Join(a.ProjectRoot, ".claude", "settings.json")
		statePath := filepath.Join(a.ProjectRoot, ".agents", ".sync", "claude-hooks-state.json")
		if a.DryRun {
			if _, err := os.Stat(hooksDir); err == nil {
				a.Info(fmt.Sprintf("[dry-run] would merge hooks into %s", settingsPath))
			}
		} else {
			n, err := a.MergeHooks(hooksDir, settingsPath, statePath)
			if err != nil {
				a.Warn(fmt.Sprintf("hooks merge: %v", err))
			} else if n > 0 {
				a.Info(fmt.Sprintf("merged %d hook(s) into %s", n, settingsPath))
			}
		}
	}

	a.updateGitignore()

	a.Info("Sync complete.")
	return nil
}

func (a *App) CmdStatus() error {
	fmt.Fprintf(a.Stdout, "sync-agents v%s\n", version.Version)
	fmt.Fprintln(a.Stdout)

	agentsDir := filepath.Join(a.ProjectRoot, ".agents")
	if fi, err := os.Stat(agentsDir); err == nil && fi.IsDir() {
		fmt.Fprintf(a.Stdout, "[ok] .agents/ exists\n")
		a.PrintTree(agentsDir, "")
	} else {
		fmt.Fprintf(a.Stdout, "[missing] .agents/ not found\n")
	}

	fmt.Fprintln(a.Stdout)

	agentsMD := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); err == nil {
		fmt.Fprintf(a.Stdout, "[ok] AGENTS.md exists\n")
	} else {
		fmt.Fprintf(a.Stdout, "[missing] AGENTS.md not found\n")
	}

	claudeMD := filepath.Join(a.ProjectRoot, "CLAUDE.md")
	fi, err := os.Lstat(claudeMD)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		linkTarget, _ := os.Readlink(claudeMD)
		fmt.Fprintf(a.Stdout, "[ok] CLAUDE.md -> %s\n", linkTarget)
	} else if err == nil {
		fmt.Fprintf(a.Stdout, "[warn] CLAUDE.md exists but is not a symlink\n")
	} else {
		fmt.Fprintf(a.Stdout, "[missing] CLAUDE.md not found\n")
	}

	fmt.Fprintln(a.Stdout)

	for _, target := range AllTargets {
		targetDir := ResolveTargetDir(target, a.ProjectRoot)
		displayDir := targetDir
		if strings.HasPrefix(targetDir, a.ProjectRoot+"/") {
			displayDir = targetDir[len(a.ProjectRoot)+1:]
		}

		rulesLink := filepath.Join(targetDir, "rules")
		hasDirOrLinks := false
		if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
			hasDirOrLinks = true
		}
		if fi, err := os.Lstat(rulesLink); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			hasDirOrLinks = true
		}

		if hasDirOrLinks {
			fmt.Fprintf(a.Stdout, "%s/\n", displayDir)
			for _, b := range Buckets {
				if !b.SyncsToLocalTarget(target) {
					continue
				}
				sub := filepath.Join(targetDir, b.Dir)
				sfi, serr := os.Lstat(sub)
				if serr == nil && sfi.Mode()&os.ModeSymlink != 0 {
					lt, _ := os.Readlink(sub)
					fmt.Fprintf(a.Stdout, "  [synced] %s -> %s\n", b.Dir, lt)
				} else if serr == nil && sfi.IsDir() {
					fmt.Fprintf(a.Stdout, "  [local] %s (not symlinked)\n", b.Dir)
				} else if b.InInit {
					// Optional buckets (agents/…) are only reported
					// when something exists for them; the classic
					// three always show, matching pre-registry output.
					fmt.Fprintf(a.Stdout, "  [missing] %s\n", b.Dir)
				} else if fi, err := os.Stat(filepath.Join(a.ProjectRoot, ".agents", b.Dir)); err == nil && fi.IsDir() {
					fmt.Fprintf(a.Stdout, "  [missing] %s\n", b.Dir)
				}
			}
		} else {
			fmt.Fprintf(a.Stdout, "[not synced] %s/\n", displayDir)
		}
	}
	return nil
}

func (a *App) CmdIndex() error {
	if err := a.EnsureAgentsDir(); err != nil {
		return err
	}
	if err := a.generateAgentsMD(); err != nil {
		return err
	}
	if a.DryRun {
		a.Info("Would regenerate AGENTS.md")
		return nil
	}
	a.Info("Regenerated AGENTS.md")
	return nil
}

func (a *App) CmdClean() error {
	a.Info("Removing synced symlinks...")

	for _, target := range a.ActiveTargets {
		targetDir := ResolveTargetDir(target, a.ProjectRoot)
		displayDir := targetDir
		if strings.HasPrefix(targetDir, a.ProjectRoot+"/") {
			displayDir = targetDir[len(a.ProjectRoot)+1:]
		}

		for _, subdir := range BucketDirs() {
			sub := filepath.Join(targetDir, subdir)
			fi, err := os.Lstat(sub)
			if err == nil && fi.Mode()&os.ModeSymlink != 0 {
				os.Remove(sub)
				a.Info(fmt.Sprintf("Removed: %s/%s", displayDir, subdir))
			}
		}

		if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
			entries, _ := os.ReadDir(targetDir)
			if len(entries) == 0 {
				os.Remove(targetDir)
				a.Info(fmt.Sprintf("Removed empty directory: %s/", displayDir))
			}
		}
	}

	claudeMD := filepath.Join(a.ProjectRoot, "CLAUDE.md")
	fi, err := os.Lstat(claudeMD)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		os.Remove(claudeMD)
		a.Info("Removed: CLAUDE.md symlink")
	}

	a.Info("Clean complete.")
	return nil
}

func (a *App) CmdWatch() error {
	if err := a.EnsureAgentsDir(); err != nil {
		return err
	}

	watchDir := filepath.Join(a.ProjectRoot, ".agents")

	if _, err := exec.LookPath("fswatch"); err == nil {
		a.Info("Watching .agents/ for changes... (Ctrl+C to stop)")
		a.CmdIndex()
		cmd := exec.Command("fswatch", "-o", watchDir)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			a.Info("Change detected, regenerating index...")
			a.CmdIndex()
		}
		return cmd.Wait()
	}

	if _, err := exec.LookPath("inotifywait"); err == nil {
		a.Info("Watching .agents/ for changes... (Ctrl+C to stop)")
		a.CmdIndex()
		cmd := exec.Command("inotifywait", "-m", "-r", "-e", "modify,create,delete,move", "--format", "%w%f", watchDir)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			a.Info("Change detected, regenerating index...")
			a.CmdIndex()
		}
		return cmd.Wait()
	}

	a.Error("Neither fswatch (macOS) nor inotifywait (Linux) found.")
	a.Error("Install with: brew install fswatch  OR  apt install inotify-tools")
	return fmt.Errorf("no watcher")
}

func (a *App) CmdImport(url string) error {
	if url == "" {
		a.Error("Usage: sync-agents import <url>")
		return fmt.Errorf("missing url")
	}

	if err := a.EnsureAgentsDir(); err != nil {
		return err
	}

	filename := filepath.Base(url)
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}

	var typ string
	for _, b := range Buckets {
		if strings.Contains(url, "/"+b.Dir+"/") {
			typ = b.Dir
			break
		}
	}

	if typ == "" {
		fmt.Fprintln(a.Stdout, "Could not detect type from URL. Choose:")
		for i, b := range Buckets {
			fmt.Fprintf(a.Stdout, "  %d) %s\n", i+1, b.Artifact)
		}
		fmt.Fprintf(a.Stdout, "Selection (1-%d): ", len(Buckets))
		var choice string
		fmt.Scanln(&choice)
		idx, err := strconv.Atoi(choice)
		if err != nil || idx < 1 || idx > len(Buckets) {
			a.Error("Invalid selection")
			return fmt.Errorf("invalid selection")
		}
		typ = Buckets[idx-1].Dir
	}

	destDir := filepath.Join(a.ProjectRoot, ".agents", typ)
	os.MkdirAll(destDir, 0755)
	dest := filepath.Join(destDir, filename)

	a.Info(fmt.Sprintf("Importing %s → .agents/%s/%s", url, typ, filename))

	cmd := exec.Command("curl", "-fsSL", url, "-o", dest)
	cmd.Stderr = a.Stderr
	if err := cmd.Run(); err != nil {
		a.Error(fmt.Sprintf("Failed to download: %s", url))
		return err
	}

	a.Info("Imported successfully.")
	a.CmdIndex()
	return nil
}

func (a *App) CmdHook() error {
	gitDir := filepath.Join(a.ProjectRoot, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		a.Error("Not a git repository (no .git/ found).")
		return fmt.Errorf("not a git repo")
	}

	hookDir := filepath.Join(gitDir, "hooks")
	os.MkdirAll(hookDir, 0755)
	hookFile := filepath.Join(hookDir, "pre-commit")

	marker := "sync-agents start"

	if data, err := os.ReadFile(hookFile); err == nil {
		if strings.Contains(string(data), marker) {
			a.Info(fmt.Sprintf("Git hook already installed in %s", hookFile))
			return nil
		}
	}

	hookBlock := `
# --- sync-agents start ---
if command -v sync-agents >/dev/null 2>&1; then
  sync-agents sync 2>/dev/null
  sync-agents index 2>/dev/null
  git add AGENTS.md CLAUDE.md .claude/ .windsurf/ .cursor/ .github/copilot/ 2>/dev/null || true
fi
# --- sync-agents end ---
`

	if _, err := os.Stat(hookFile); err == nil {
		f, err := os.OpenFile(hookFile, os.O_APPEND|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		f.WriteString(hookBlock)
		f.Close()
		a.Info(fmt.Sprintf("Appended sync-agents hook to existing %s", hookFile))
	} else {
		content := "#!/bin/sh\n" + hookBlock + "\n"
		os.WriteFile(hookFile, []byte(content), 0755)
		a.Info(fmt.Sprintf("Created git hook: %s", hookFile))
	}
	return nil
}

func (a *App) CmdFix(fixType string, noClobber bool) error {
	if err := a.EnsureAgentsDir(); err != nil {
		return err
	}

	var subdirs []string
	if fixType == "all" || fixType == "" {
		subdirs = BucketDirs()
	} else if b, ok := BucketForDir(fixType); ok {
		subdirs = []string{b.Dir}
	} else {
		a.Error(fmt.Sprintf("Unknown type: %s (expected: %s, or all)", fixType, strings.Join(BucketDirs(), ", ")))
		return fmt.Errorf("unknown type")
	}

	agentsAbs, _ := filepath.Abs(filepath.Join(a.ProjectRoot, ".agents"))
	fixed := 0
	skipped := 0
	merged := 0

	// Phase 1: Migrate legacy dirs
	for _, subdir := range subdirs {
		legacyDir := filepath.Join(a.ProjectRoot, subdir)
		agentsSubdir := filepath.Join(agentsAbs, subdir)

		fi, err := os.Lstat(legacyDir)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			a.Info(fmt.Sprintf("%s/ is already a symlink — nothing to do.", subdir))
			continue
		}
		if !fi.IsDir() {
			continue
		}

		// Check same inode
		if sameInode(legacyDir, agentsSubdir) {
			a.Warn(fmt.Sprintf("%s/ and .agents/%s/ are the same directory (same inode).", subdir, subdir))
			a.Warn(fmt.Sprintf("Replacing %s/ with a symlink to .agents/%s/.", subdir, subdir))
			if a.DryRun {
				fmt.Fprintf(a.Stdout, "  would remove %s/ (same inode as .agents/%s/)\n", subdir, subdir)
				fmt.Fprintf(a.Stdout, "  would create symlink %s/ -> .agents/%s\n", subdir, subdir)
			} else {
				os.RemoveAll(legacyDir)
				os.Symlink(".agents/"+subdir, legacyDir)
				a.Info(fmt.Sprintf("Replaced %s/ with symlink -> .agents/%s", subdir, subdir))
			}
			fixed++
			continue
		}

		a.Info(fmt.Sprintf("Found legacy directory: %s/", subdir))
		os.MkdirAll(agentsSubdir, 0755)

		// Move directories
		dirEntries, _ := os.ReadDir(legacyDir)
		for _, entry := range dirEntries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			dest := filepath.Join(agentsSubdir, name)

			if _, err := os.Stat(dest); err == nil {
				if noClobber {
					a.Warn(fmt.Sprintf("Skipping %s/%s — already exists in .agents/%s/ (--no-clobber)", subdir, name, subdir))
					skipped++
					continue
				}
				if a.DryRun {
					fmt.Fprintf(a.Stdout, "  would merge: %s/%s -> .agents/%s/%s (overwrite)\n", subdir, name, subdir, name)
				} else {
					os.RemoveAll(dest)
					os.Rename(filepath.Join(legacyDir, name), dest)
					a.Info(fmt.Sprintf("Merged: %s/%s -> .agents/%s/%s (overwrote existing)", subdir, name, subdir, name))
				}
				merged++
				fixed++
				continue
			}

			if a.DryRun {
				fmt.Fprintf(a.Stdout, "  would move: %s/%s -> .agents/%s/%s\n", subdir, name, subdir, name)
			} else {
				os.Rename(filepath.Join(legacyDir, name), dest)
				a.Info(fmt.Sprintf("Moved: %s/%s -> .agents/%s/%s", subdir, name, subdir, name))
			}
			fixed++
		}

		// Move files
		dirEntries, _ = os.ReadDir(legacyDir)
		for _, entry := range dirEntries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			dest := filepath.Join(agentsSubdir, name)

			if _, err := os.Stat(dest); err == nil {
				if noClobber {
					a.Warn(fmt.Sprintf("Skipping %s/%s — already exists in .agents/%s/ (--no-clobber)", subdir, name, subdir))
					skipped++
					continue
				}
				if a.DryRun {
					fmt.Fprintf(a.Stdout, "  would merge: %s/%s -> .agents/%s/%s (overwrite)\n", subdir, name, subdir, name)
				} else {
					os.Rename(filepath.Join(legacyDir, name), dest)
					a.Info(fmt.Sprintf("Merged: %s/%s -> .agents/%s/%s (overwrote existing)", subdir, name, subdir, name))
				}
				merged++
				fixed++
				continue
			}

			if a.DryRun {
				fmt.Fprintf(a.Stdout, "  would move: %s/%s -> .agents/%s/%s\n", subdir, name, subdir, name)
			} else {
				os.Rename(filepath.Join(legacyDir, name), dest)
				a.Info(fmt.Sprintf("Moved: %s/%s -> .agents/%s/%s", subdir, name, subdir, name))
			}
			fixed++
		}

		// Replace legacy dir with symlink
		if a.DryRun {
			fmt.Fprintf(a.Stdout, "  would replace %s/ with symlink -> .agents/%s\n", subdir, subdir)
		} else {
			remaining, _ := os.ReadDir(legacyDir)
			if len(remaining) == 0 {
				os.Remove(legacyDir)
				os.Symlink(".agents/"+subdir, legacyDir)
				a.Info(fmt.Sprintf("Replaced %s/ with symlink -> .agents/%s", subdir, subdir))
			} else {
				a.Warn(fmt.Sprintf("%s/ is not empty after migration — skipping symlink replacement", subdir))
			}
		}
	}

	// Phase 1b: Convert flat skill files to directory layout
	for _, subdir := range subdirs {
		if subdir != "skills" {
			continue
		}
		skillsDir := filepath.Join(agentsAbs, "skills")
		if _, err := os.Stat(skillsDir); err != nil {
			continue
		}

		entries, _ := os.ReadDir(skillsDir)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".md") {
				continue
			}
			baseName := strings.TrimSuffix(name, ".md")
			targetDir := filepath.Join(skillsDir, baseName)
			targetFile := filepath.Join(targetDir, "SKILL.md")

			if _, err := os.Stat(targetDir); err == nil {
				if _, err := os.Stat(targetFile); err == nil {
					if noClobber {
						a.Warn(fmt.Sprintf("Skipping flat skill %s — %s/SKILL.md already exists (--no-clobber)", name, baseName))
						skipped++
						continue
					}
					if a.DryRun {
						fmt.Fprintf(a.Stdout, "  would convert: skills/%s -> skills/%s/SKILL.md (overwrite)\n", name, baseName)
					} else {
						os.Rename(filepath.Join(skillsDir, name), targetFile)
						a.Info(fmt.Sprintf("Converted: skills/%s -> skills/%s/SKILL.md (overwrote existing)", name, baseName))
					}
					merged++
					fixed++
					continue
				}
			}

			if a.DryRun {
				fmt.Fprintf(a.Stdout, "  would convert: skills/%s -> skills/%s/SKILL.md\n", name, baseName)
			} else {
				os.MkdirAll(targetDir, 0755)
				os.Rename(filepath.Join(skillsDir, name), targetFile)
				a.Info(fmt.Sprintf("Converted: skills/%s -> skills/%s/SKILL.md", name, baseName))
			}
			fixed++
		}
	}

	// Phase 2: Repair broken/missing symlinks
	repaired := 0
	for _, target := range a.ActiveTargets {
		targetDir := ResolveTargetDir(target, a.ProjectRoot)
		agentsRel := ResolveAgentsRel(target)

		for _, subdir := range subdirs {
			if _, err := os.Stat(filepath.Join(agentsAbs, subdir)); err != nil {
				continue
			}
			expectedLink := filepath.Join(targetDir, subdir)
			expectedSource := agentsRel + "/" + subdir

			fi, err := os.Lstat(expectedLink)
			if err == nil && fi.Mode()&os.ModeSymlink != 0 {
				currentTarget, _ := os.Readlink(expectedLink)
				if currentTarget == expectedSource {
					continue
				}
				if a.DryRun {
					fmt.Fprintf(a.Stdout, "  would relink: %s -> %s (was %s)\n", expectedLink, expectedSource, currentTarget)
				} else {
					os.Remove(expectedLink)
					a.CreateSymlink(expectedSource, expectedLink, false)
					a.Info(fmt.Sprintf("Repaired: %s -> %s (was %s)", expectedLink, expectedSource, currentTarget))
				}
				repaired++
			} else if err == nil {
				if a.Force {
					if a.DryRun {
						fmt.Fprintf(a.Stdout, "  would replace: %s with symlink -> %s\n", expectedLink, expectedSource)
					} else {
						os.RemoveAll(expectedLink)
						a.CreateSymlink(expectedSource, expectedLink, false)
						a.Info(fmt.Sprintf("Repaired: replaced %s with symlink -> %s", expectedLink, expectedSource))
					}
					repaired++
				} else {
					a.Warn(fmt.Sprintf("%s exists but is not a symlink (use --force to replace)", expectedLink))
				}
			} else {
				if a.DryRun {
					fmt.Fprintf(a.Stdout, "  would create: %s -> %s\n", expectedLink, expectedSource)
				} else {
					a.CreateSymlink(expectedSource, expectedLink, false)
				}
				repaired++
			}
		}
	}

	// Repair CLAUDE.md symlink
	agentsMDPath := filepath.Join(a.ProjectRoot, "AGENTS.md")
	claudeMDPath := filepath.Join(a.ProjectRoot, "CLAUDE.md")
	if _, err := os.Stat(agentsMDPath); err == nil {
		fi, err := os.Lstat(claudeMDPath)
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			currentTarget, _ := os.Readlink(claudeMDPath)
			if currentTarget != "AGENTS.md" {
				if a.DryRun {
					fmt.Fprintf(a.Stdout, "  would relink: CLAUDE.md -> AGENTS.md (was %s)\n", currentTarget)
				} else {
					os.Remove(claudeMDPath)
					a.CreateSymlink("AGENTS.md", claudeMDPath, false)
				}
				repaired++
			}
		} else if os.IsNotExist(err) || (err != nil) {
			if a.DryRun {
				fmt.Fprintf(a.Stdout, "  would create: CLAUDE.md -> AGENTS.md\n")
			} else {
				a.CreateSymlink("AGENTS.md", claudeMDPath, false)
			}
			repaired++
		}
	}

	// Phase 3: Migrate legacy STATE.md
	stateMigrated := 0
	legacyStatePath := filepath.Join(agentsAbs, "STATE.md")
	if _, err := os.Stat(legacyStatePath); err == nil {
		stateRulePath := filepath.Join(agentsAbs, "rules", "state.md")
		if _, err := os.Stat(stateRulePath); os.IsNotExist(err) {
			if a.DryRun {
				fmt.Fprintf(a.Stdout, "  would create: .agents/rules/state.md from template\n")
			} else {
				os.MkdirAll(filepath.Join(agentsAbs, "rules"), 0755)
				os.WriteFile(stateRulePath, []byte(templates.State()), 0644)
				a.Info("Created .agents/rules/state.md (state convention rule)")
			}
		}
		if a.DryRun {
			fmt.Fprintf(a.Stdout, "  would migrate: .agents/STATE.md → per-file state pattern\n")
		} else {
			a.migrateLegacyState(agentsAbs)
		}
		stateMigrated = 1
	}

	// Summary
	if fixed == 0 && skipped == 0 && repaired == 0 && stateMigrated == 0 {
		a.Info("Nothing to fix — all directories and symlinks are correct.")
	} else {
		if fixed > 0 {
			a.Info(fmt.Sprintf("Fixed %d item(s).", fixed))
		}
		if merged > 0 {
			a.Info(fmt.Sprintf("Merged %d item(s) (legacy overwrote existing).", merged))
		}
		if skipped > 0 {
			a.Warn(fmt.Sprintf("Skipped %d item(s) (use without --no-clobber to merge).", skipped))
		}
		if repaired > 0 {
			a.Info(fmt.Sprintf("Repaired %d symlink(s).", repaired))
		}
		if stateMigrated > 0 {
			a.Info("Migrated legacy STATE.md to per-file state pattern.")
		}
		if fixed > 0 {
			a.Info("Run 'sync-agents sync' to update agent target symlinks.")
		}
	}
	return nil
}

func (a *App) CmdInheritList() error {
	agentsMD := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); os.IsNotExist(err) {
		a.Info("No AGENTS.md found.")
		return nil
	}

	data, err := os.ReadFile(agentsMD)
	if err != nil {
		return err
	}

	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		if regexp.MustCompile(`^##\s+Inherits`).MatchString(line) {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if inSection && regexp.MustCompile(`^-\s+\[`).MatchString(line) {
			fmt.Fprintln(a.Stdout, line)
		}
	}
	return nil
}

func (a *App) CmdInheritRemove(label string) error {
	if label == "" {
		a.Error("Usage: sync-agents inherit --remove <label>")
		return fmt.Errorf("missing label")
	}

	agentsMD := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); os.IsNotExist(err) {
		a.Error("No AGENTS.md found.")
		return fmt.Errorf("no AGENTS.md")
	}

	data, err := os.ReadFile(agentsMD)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	inSection := false
	removed := false

	for _, line := range lines {
		if regexp.MustCompile(`^##\s+Inherits`).MatchString(line) {
			inSection = true
			result = append(result, line)
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			inSection = false
		}
		if inSection && strings.Contains(line, "["+label+"](") {
			removed = true
			continue
		}
		result = append(result, line)
	}

	os.WriteFile(agentsMD, []byte(strings.Join(result, "\n")), 0644)
	if removed {
		a.Info(fmt.Sprintf("Removed inherit: %s", label))
	} else {
		a.Warn(fmt.Sprintf("No inherit found with label: %s", label))
	}
	return nil
}

func (a *App) CmdInheritAdd(label, path string) error {
	if label == "" || path == "" {
		a.Error("Usage: sync-agents inherit <label> <path>")
		a.Error("       sync-agents inherit --list")
		a.Error("       sync-agents inherit --remove <label>")
		return fmt.Errorf("missing args")
	}

	// Validate path
	resolvedPath := path
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") {
		resolvedPath = strings.Replace(path, "~", os.Getenv("HOME"), 1)
	} else {
		resolvedPath = filepath.Join(a.ProjectRoot, path)
	}
	if _, err := os.Stat(resolvedPath); err != nil {
		a.Warn(fmt.Sprintf("Path does not exist: %s (link will be added anyway)", path))
	}

	agentsMD := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); os.IsNotExist(err) {
		a.Error("No AGENTS.md found. Run 'sync-agents init' first.")
		return fmt.Errorf("no AGENTS.md")
	}

	data, err := os.ReadFile(agentsMD)
	if err != nil {
		return err
	}
	content := string(data)

	// Check for duplicate
	if strings.Contains(content, "["+label+"](") {
		a.Warn(fmt.Sprintf("Inherit with label '%s' already exists. Use --remove first to update.", label))
		return fmt.Errorf("duplicate")
	}

	lines := strings.Split(content, "\n")
	entry := fmt.Sprintf("- [%s](%s)", label, path)

	if !strings.Contains(content, "## Inherits") {
		// Insert Inherits section after description
		var result []string
		headerDone := false
		inheritsWritten := false
		for _, line := range lines {
			result = append(result, line)
			if !headerDone && strings.HasPrefix(line, "This file indexes") {
				headerDone = true
				result = append(result, "")
				result = append(result, "## Inherits")
				result = append(result, "")
				result = append(result, entry)
				inheritsWritten = true
			}
		}
		if !inheritsWritten {
			// Fallback: insert before ## Rules
			result = nil
			for _, line := range lines {
				if line == "## Rules" && !inheritsWritten {
					result = append(result, "## Inherits")
					result = append(result, "")
					result = append(result, entry)
					result = append(result, "")
					inheritsWritten = true
				}
				result = append(result, line)
			}
		}
		os.WriteFile(agentsMD, []byte(strings.Join(result, "\n")), 0644)
	} else {
		// Append to existing Inherits section
		var result []string
		inSection := false
		added := false
		for _, line := range lines {
			if regexp.MustCompile(`^##\s+Inherits`).MatchString(line) {
				inSection = true
				result = append(result, line)
				continue
			}
			if inSection && !added {
				if strings.HasPrefix(line, "## ") {
					result = append(result, entry)
					result = append(result, "")
					added = true
					inSection = false
				} else if regexp.MustCompile(`^-\s+\[`).MatchString(line) {
					result = append(result, line)
					continue
				} else if line == "" {
					result = append(result, entry)
					added = true
					inSection = false
					result = append(result, line)
					continue
				}
			}
			result = append(result, line)
		}
		if !added {
			result = append(result, entry)
			result = append(result, "")
		}
		os.WriteFile(agentsMD, []byte(strings.Join(result, "\n")), 0644)
	}

	a.Info(fmt.Sprintf("Added inherit: [%s](%s)", label, path))
	return nil
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func (a *App) migrateLegacyState(agentsDir string) {
	legacy := filepath.Join(agentsDir, "STATE.md")
	if _, err := os.Stat(legacy); err != nil {
		return
	}

	data, err := os.ReadFile(legacy)
	if err != nil {
		return
	}

	// Check for meaningful content
	contentLines := 0
	boilerplate := regexp.MustCompile(`^(---|trigger:|#|$|Track project|Update this|Be sure|Description of|Save both|A new file|STATE HISTORY)`)
	for _, line := range strings.Split(string(data), "\n") {
		if !boilerplate.MatchString(line) {
			contentLines++
		}
	}

	if contentLines > 0 {
		timestamp := time.Now().Format("20060102150405")
		migrated := filepath.Join(agentsDir, fmt.Sprintf("STATE_legacy-history_%s.md", timestamp))
		os.WriteFile(migrated, data, 0644)
		a.Info(fmt.Sprintf("Migrated legacy STATE.md history → %s", filepath.Base(migrated)))
	}

	os.Remove(legacy)
	a.Info("Removed legacy .agents/STATE.md (replaced by rules/state.md pattern)")
}

func (a *App) generateAgentsMD() error {
	outfile := filepath.Join(a.ProjectRoot, "AGENTS.md")
	agentsDir := filepath.Join(a.ProjectRoot, ".agents")

	// Preserve Inherits section
	inheritsBlock := ""
	if data, err := os.ReadFile(outfile); err == nil {
		inSection := false
		for _, line := range strings.Split(string(data), "\n") {
			if regexp.MustCompile(`^##\s+Inherits`).MatchString(line) {
				inSection = true
				inheritsBlock += line + "\n"
				continue
			}
			if inSection && strings.HasPrefix(line, "## ") {
				break
			}
			if inSection {
				inheritsBlock += line + "\n"
			}
		}
	}

	var b strings.Builder
	b.WriteString("---\ntrigger: always_on\n---\n\n# AGENTS\n\n")
	b.WriteString("> Auto-generated by [sync-agents](https://github.com/brickhouse-tech/sync-agents). Do not edit manually.\n")
	b.WriteString("> Run `sync-agents index` to regenerate.\n\n")
	b.WriteString("This file indexes all rules, skills, and workflows defined in `.agents/`.\n\n")

	// Hand-authored global content, injected verbatim between the
	// header and the generated index sections. Absent by default —
	// see preamble.go and SPEC-007.
	if preamble, ok := a.ReadPreamble(); ok {
		b.WriteString(PreambleMarker)
		b.WriteString("\n\n")
		b.WriteString(preamble)
		b.WriteString("\n\n")
		if a.DryRun {
			fmt.Fprintf(a.Stdout, "  would inject preamble: %s\n", a.ResolvePreamblePath())
		}
	}

	if inheritsBlock != "" {
		// Trim captured trailing blank lines before re-adding the
		// section separator — otherwise every regeneration appends
		// one more blank line (index must be idempotent).
		b.WriteString(strings.TrimRight(inheritsBlock, "\n"))
		b.WriteString("\n\n")
	}

	// Rules
	b.WriteString("## Rules\n\n")
	rulesDir := filepath.Join(agentsDir, "rules")
	ruleFiles := listMDFiles(rulesDir)
	if len(ruleFiles) > 0 {
		for _, name := range ruleFiles {
			b.WriteString(indexEntry(name, BucketLinkPrefix(a.ProjectRoot, agentsDir, "rules")+"/"+name+".md", filepath.Join(rulesDir, name+".md")))
		}
	} else {
		b.WriteString("_No rules defined yet. Add one with `sync-agents add rule <name>`._\n")
	}
	b.WriteString("\n")

	// Skills
	b.WriteString("## Skills\n\n")
	hasSkills := false
	skillsDir := filepath.Join(agentsDir, "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			skillFile := filepath.Join(skillsDir, name, "SKILL.md")
			if _, err := os.Stat(skillFile); err == nil {
				b.WriteString(indexEntry(name, BucketLinkPrefix(a.ProjectRoot, agentsDir, "skills")+"/"+name+"/SKILL.md", skillFile))
				hasSkills = true
			}
		}
		// Legacy flat skills
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasSuffix(name, ".md") {
				baseName := strings.TrimSuffix(name, ".md")
				b.WriteString(fmt.Sprintf("- [%s](.agents/skills/%s.md)\n", baseName, baseName))
				hasSkills = true
			}
		}
	}
	if !hasSkills {
		b.WriteString("_No skills defined yet. Add one with `sync-agents add skill <name>`._\n")
	}
	b.WriteString("\n")

	// Workflows
	b.WriteString("## Workflows\n\n")
	workflowsDir := filepath.Join(agentsDir, "workflows")
	wfFiles := listMDFiles(workflowsDir)
	if len(wfFiles) > 0 {
		for _, name := range wfFiles {
			b.WriteString(indexEntry(name, BucketLinkPrefix(a.ProjectRoot, agentsDir, "workflows")+"/"+name+".md", filepath.Join(workflowsDir, name+".md")))
		}
	} else {
		b.WriteString("_No workflows defined yet. Add one with `sync-agents add workflow <name>`._\n")
	}
	b.WriteString("\n")

	// Agents (subagents) — optional bucket, section appears only
	// when at least one definition exists (SPEC-004 backwards
	// compatibility: index gains sections only for present buckets).
	agentsBucketDir := filepath.Join(agentsDir, "agents")
	if agentFiles := listMDFiles(agentsBucketDir); len(agentFiles) > 0 {
		b.WriteString("## Agents\n\n")
		for _, name := range agentFiles {
			b.WriteString(indexEntry(name, BucketLinkPrefix(a.ProjectRoot, agentsDir, "agents")+"/"+name+".md", filepath.Join(agentsBucketDir, name+".md")))
		}
		b.WriteString("\n")
	}

	// Reference-doc buckets (SPEC-004 Part D): plans and specs.
	// Optional sections like Agents, but listed recursively because
	// these documents are commonly grouped per effort in subdirs.
	for _, ref := range []struct{ title, dir string }{
		{"Plans", "plans"},
		{"Specs", "specs"},
	} {
		refDir := filepath.Join(agentsDir, ref.dir)
		files, warns := listMDFilesRecursive(refDir)
		for _, w := range warns {
			a.Warn(w)
		}
		if len(files) == 0 {
			continue
		}
		b.WriteString("## " + ref.title + "\n\n")
		for _, rel := range files {
			link := BucketLinkPrefix(a.ProjectRoot, agentsDir, ref.dir) + "/" + rel + ".md"
			b.WriteString(indexEntry(rel, link, filepath.Join(refDir, filepath.FromSlash(rel)+".md")))
		}
		b.WriteString("\n")
	}

	// Hooks (SPEC-004 Part C)
	hooksBucketDir := filepath.Join(agentsDir, "hooks")
	if entries, err := os.ReadDir(hooksBucketDir); err == nil {
		var hookFiles []string
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
				hookFiles = append(hookFiles, e.Name())
			}
		}
		if len(hookFiles) > 0 {
			sort.Strings(hookFiles)
			b.WriteString("## Hooks\n\n")
			for _, name := range hookFiles {
				link := BucketLinkPrefix(a.ProjectRoot, agentsDir, "hooks") + "/" + name
				srcPath := filepath.Join(hooksBucketDir, name)
				// Hook fragments don't have markdown frontmatter for
				// description — just link the file.
				b.WriteString(fmt.Sprintf("- [%s](%s)\n", name, link))
				_ = srcPath
			}
			b.WriteString("\n")
		}
	}

	// State
	b.WriteString("## State\n\n")
	hasState := false
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, "STATE_") && strings.HasSuffix(name, ".md") {
				baseName := strings.TrimSuffix(name, ".md")
				b.WriteString(fmt.Sprintf("- [%s](.agents/%s)\n", baseName, name))
				hasState = true
			}
		}
	}
	legacyState := filepath.Join(agentsDir, "STATE.md")
	if _, err := os.Stat(legacyState); err == nil {
		b.WriteString("- [STATE.md](.agents/STATE.md)\n")
		hasState = true
	}
	if !hasState {
		b.WriteString("_No state snapshots yet. Agents will create STATE_*context*_*timestamp*.md files as they work._\n")
	}
	b.WriteString("\n")

	// Managed @-import block for Claude. Claude doesn't auto-scan
	// .claude/rules/*.md and doesn't follow markdown links in
	// AGENTS.md/CLAUDE.md, so the only reliable mechanism to get
	// rule content into Claude's context is the `@`-import syntax.
	// The block is fully regenerated each index (stateless); same
	// .agents/ state produces the same bytes — idempotent and safe
	// to re-run on every `sync-agents index` / `sync-agents sync`.
	//
	// Paths are project-relative (.claude/rules/<name>.md) because
	// AGENTS.md is checked into git; absolute paths wouldn't port
	// across developers. Claude resolves @-import symlinks, so
	// `.claude/rules/X.md` (which is a symlink to
	// `.agents/rules/X.md`) loads the same content.
	var localArts []ClaudeRoutedArtifact
	for _, name := range ruleFiles {
		localArts = append(localArts, ClaudeRoutedArtifact{
			Type:     ArtifactRule,
			Name:     name,
			Semantic: Passive, // bucket default: rules are passive
		})
	}
	for _, name := range wfFiles {
		// Bucket default for workflows is Invocable, but
		// Claude's workflow destination is commands/<name>.md
		// which Claude already auto-registers — so passive
		// workflows aren't relevant to local CLAUDE.md for the
		// common case. We still include workflows that users
		// have explicitly marked passive via frontmatter.
		wfPath := filepath.Join(agentsDir, "workflows", name+".md")
		sem, err := ResolveSemantic(wfPath, ArtifactWorkflow)
		if err != nil || sem != Passive {
			continue
		}
		localArts = append(localArts, ClaudeRoutedArtifact{
			Type:     ArtifactWorkflow,
			Name:     name,
			Semantic: sem,
		})
	}
	// No @-import block here. @-imports are Claude's mechanism for
	// loading rules, and global sync writes them straight into
	// CLAUDE.md (see RegenerateClaudeImports). Emitting them into
	// AGENTS.md as well put a second copy into CLAUDE.md by way of
	// the mirror, so every rule loaded twice in every session. Other
	// agents read the markdown links in the index above; they have no
	// use for @-imports either.

	// Strip any import block an earlier version of this generator
	// wrote into AGENTS.md. Unconditional: the block is never emitted
	// now, so one found here is always stale.
	finalContent := b.String()
	{
		if existing, err := os.ReadFile(outfile); err == nil {
			if HasManagedImportBlock(string(existing)) {
				startIdx := strings.Index(string(existing), ManagedImportBlockStart)
				endIdx := strings.Index(string(existing)[startIdx:], ManagedImportBlockEnd)
				if startIdx >= 0 && endIdx >= 0 {
					endFull := startIdx + endIdx + len(ManagedImportBlockEnd)
					if endFull < len(existing) && existing[endFull] == '\n' {
						endFull++
					}
					finalContent = string(existing[:startIdx]) + string(existing[endFull:])
				}
			}
		}
	}

	// A dry run must never touch the filesystem. This guard lives here
	// rather than at each call site so every path that regenerates the
	// index inherits it.
	if a.DryRun {
		fmt.Fprintf(a.Stdout, "  would write: %s\n", outfile)
		return nil
	}

	// Refuse to regenerate an index that would drop preamble content
	// the existing file has. The preamble is usually most of AGENTS.md
	// and its loss is silent, so this fails loudly instead.
	if preambleWouldBeDropped(outfile, finalContent) {
		fmt.Fprintf(a.Stderr, "        expected the preamble at: %s\n", a.ResolvePreamblePath())
		fmt.Fprintf(a.Stderr, "        regenerating now would delete it; check --global-root and $SYNC_AGENTS_GLOBAL_ROOT, or restore the file\n")
		return fmt.Errorf("refusing to write %s: it contains preamble content and this run produced none", outfile)
	}

	os.WriteFile(outfile, []byte(finalContent), 0644)
	return nil
}

func listMDFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".md") {
			names = append(names, strings.TrimSuffix(name, ".md"))
		}
	}
	sort.Strings(names)
	return names
}

// listMDFilesRecursive returns the .md files under dir at any depth,
// as slash-separated paths relative to dir with the extension
// stripped ("effort-x/plan-a"). Reference buckets (plans/specs)
// allow grouping documents per effort in subdirectories, so their
// index sections list recursively (SPEC-004 Part D).
//
// The second return value is a slice of non-fatal warning messages
// (permission errors, unreadable files) for the caller to surface.
func listMDFilesRecursive(dir string) ([]string, []string) {
	var names []string
	var warns []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			return nil
		}
		re, err := filepath.Rel(dir, path)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		names = append(names, strings.TrimSuffix(filepath.ToSlash(re), ".md"))
		return nil
	})
	sort.Strings(names)
	return names, warns
}

// artifactDescription extracts the frontmatter `description` of the
// markdown file at path for display in the AGENTS.md index. Returns
// "" (no suffix rendered) when the file has no frontmatter, the
// description is empty or a multi-line scalar, or it is an
// unfinished scaffold stub (starts with "TODO"). Long descriptions
// are truncated so one artifact can't dominate the index.
func artifactDescription(path string) string {
	const maxIndexDescription = 140
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	block, err := parseFMBlock(string(raw))
	if err != nil || !block.present {
		return ""
	}
	desc, _ := block.get("description")
	if desc == "" || strings.HasPrefix(desc, "|") || strings.HasPrefix(desc, ">") || strings.HasPrefix(desc, "TODO") {
		return ""
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if len(desc) > maxIndexDescription {
		desc = truncateAtWord(desc, maxIndexDescription) + "…"
	}
	return desc
}

// indexEntry renders one AGENTS.md index line: `- [name](link)` with
// an ` — description` suffix when the artifact declares one.
func indexEntry(name, link, srcPath string) string {
	if desc := artifactDescription(srcPath); desc != "" {
		return fmt.Sprintf("- [%s](%s) — %s\n", name, link, desc)
	}
	return fmt.Sprintf("- [%s](%s)\n", name, link)
}

// isBucketActive reports whether the target with the given ID is in
// ActiveTargets. Used to gate hooks merge/local-sync operations.
func (a *App) isBucketActive(targetID string) bool {
	for _, t := range a.ActiveTargets {
		if t == targetID {
			return true
		}
	}
	return false
}

func (a *App) addDefaultGitignoreEntries() {
	gitignore := filepath.Join(a.ProjectRoot, ".gitignore")

	// Create if not exists
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		os.WriteFile(gitignore, []byte{}, 0644)
		a.Info("Created .gitignore")
	}

	data, _ := os.ReadFile(gitignore)
	content := string(data)

	// Add .DS_Store
	if !regexp.MustCompile(`(?i)^\.DS_Store$`).MatchString(content) {
		hasDS := false
		for _, line := range strings.Split(content, "\n") {
			if strings.EqualFold(strings.TrimSpace(line), ".DS_Store") {
				hasDS = true
				break
			}
		}
		if !hasDS {
			if len(content) > 0 && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			content += ".DS_Store\n"
			a.Info("Added .DS_Store to .gitignore")
		}
	}

	marker := "# sync-agents — ignore tool artifacts, keep symlinks"
	sectionEntries := []string{
		".cursor/*",
		"!.cursor/rules",
		".codex/*",
		"!.codex/instructions.md",
		".github/copilot/*",
		"!.github/copilot/instructions.md",
	}

	if strings.Contains(content, marker) {
		needsUpdate := false
		for _, entry := range sectionEntries {
			if !strings.Contains(content, entry) {
				needsUpdate = true
				break
			}
		}
		if needsUpdate {
			var result []string
			inSection := false
			for _, line := range strings.Split(content, "\n") {
				if line == marker {
					inSection = true
					result = append(result, line)
					result = append(result, sectionEntries...)
					continue
				}
				if inSection {
					if line == "" || strings.HasPrefix(line, "#") {
						inSection = false
						result = append(result, line)
					}
					continue
				}
				result = append(result, line)
			}
			content = strings.Join(result, "\n")
			a.Info("Updated sync-agents section in .gitignore")
		}
	} else {
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += marker + "\n"
		for _, entry := range sectionEntries {
			content += entry + "\n"
		}
		a.Info(fmt.Sprintf("Added sync-agents section to .gitignore with %d entries", len(sectionEntries)+1))
	}

	os.WriteFile(gitignore, []byte(content), 0644)
}

func (a *App) updateGitignore() {
	gitignore := filepath.Join(a.ProjectRoot, ".gitignore")

	var entries []string
	for _, target := range a.ActiveTargets {
		targetDir := ResolveTargetDir(target, a.ProjectRoot)
		rel := targetDir
		if strings.HasPrefix(targetDir, a.ProjectRoot+"/") {
			rel = targetDir[len(a.ProjectRoot)+1:]
		}
		entries = append(entries, rel+"/")
	}
	entries = append(entries, "CLAUDE.md")

	if a.DryRun {
		data, _ := os.ReadFile(gitignore)
		content := string(data)
		for _, entry := range entries {
			if !containsExactLine(content, entry) {
				fmt.Fprintf(a.Stdout, "  would add to .gitignore: %s\n", entry)
			}
		}
		return
	}

	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		os.WriteFile(gitignore, []byte{}, 0644)
	}

	data, _ := os.ReadFile(gitignore)
	content := string(data)

	added := 0
	for _, entry := range entries {
		if !containsExactLine(content, entry) {
			if added == 0 {
				if !strings.Contains(content, "# sync-agents") {
					if len(content) > 0 && content != "" {
						if !strings.HasSuffix(content, "\n") {
							content += "\n"
						}
						content += "\n"
					}
					content += "# sync-agents (generated symlinks)\n"
				}
			}
			content += entry + "\n"
			added++
		}
	}

	if added > 0 {
		os.WriteFile(gitignore, []byte(content), 0644)
		a.Info(fmt.Sprintf("Added %d entries to .gitignore", added))
	}
}

func containsExactLine(content, line string) bool {
	for _, l := range strings.Split(content, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func sameInode(path1, path2 string) bool {
	fi1, err := os.Stat(path1)
	if err != nil {
		return false
	}
	fi2, err := os.Stat(path2)
	if err != nil {
		return false
	}
	return os.SameFile(fi1, fi2)
}
