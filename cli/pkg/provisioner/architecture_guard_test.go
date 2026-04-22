package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Architecture guards: enforce invariants the audit kept re-surfacing. Each
// rule greps the on-disk source and fails loudly if a known anti-pattern
// shows up again. Cheap to run, catches regressions pre-merge.
//
// Scope is repo-wide for rules that apply to the declarative-task layer
// generally (no raw unarchive literals, no compat narration in comments).
// Rules that only make sense for provisioner call sites (no GetBinaryURL,
// no ExecuteScript-for-install, no running-state skip gate) stay scoped.

// repoSourceRoots returns the absolute paths of the trees this guard walks.
// Derived from runtime.Caller so the tests work regardless of CWD.
func repoSourceRoots(t *testing.T) (provisionerDir, ansibleDir string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	provisionerDir = filepath.Dir(thisFile)
	cliPkg := filepath.Dir(provisionerDir)
	ansibleDir = filepath.Join(cliPkg, "ansible")
	return provisionerDir, ansibleDir
}

func sourceFilesIn(t *testing.T, roots ...string) []string {
	t.Helper()
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return files
}

func scanSources(t *testing.T, rule string, files []string, pattern *regexp.Regexp, allow func(path, line string) bool) {
	t.Helper()
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !pattern.MatchString(line) {
				continue
			}
			if allow != nil && allow(path, line) {
				continue
			}
			t.Errorf("[%s] %s:%d violates rule: %s", rule, filepath.Base(path), i+1, strings.TrimSpace(line))
		}
	}
}

func TestArchitectureGuard_noGetBinaryURLInProvisioners(t *testing.T) {
	t.Parallel()
	prov, _ := repoSourceRoots(t)
	pat := regexp.MustCompile(`\bGetBinaryURL\(`)
	scanSources(t, "use-GetBinary", sourceFilesIn(t, prov), pat, nil)
}

func TestArchitectureGuard_noInstallViaExecuteScript(t *testing.T) {
	t.Parallel()
	// Install paths must go through the declarative task layer. ExecuteScript
	// remains available on BaseProvisioner for legitimate non-install
	// operations (DNS config, PKI polling, launchd sudo proxy). Anything new
	// that wants to run a shell installer must migrate instead.
	prov, _ := repoSourceRoots(t)
	allowListPaths := map[string]bool{
		"base.go":      true, // ExecuteScript definition
		"edge.go":      true, // ExecuteSudoScript is the sudo proxy for launchd/macOS, not an installer
		"privateer.go": true, // PKI-readiness wait + system DNS config, not install
	}
	pat := regexp.MustCompile(`\.ExecuteScript\(|\.ExecuteSudoScript\(`)
	scanSources(t, "no-shell-install-in-provisioner", sourceFilesIn(t, prov), pat, func(path, line string) bool {
		return allowListPaths[filepath.Base(path)]
	})
}

func TestArchitectureGuard_noRawUnarchiveLiterals(t *testing.T) {
	t.Parallel()
	// Both provisioner and ansible packages must go through TaskUnarchive so
	// the non-empty creates-sentinel guardrail applies uniformly. The helper
	// that emits the module name (tasks.go) and the linter that inspects it
	// (lint.go) are the canonical definitions and are allow-listed.
	prov, ansible := repoSourceRoots(t)
	allowListPaths := map[string]bool{
		"tasks.go": true,
		"lint.go":  true,
	}
	pat := regexp.MustCompile(`"ansible\.builtin\.unarchive"`)
	scanSources(t, "use-TaskUnarchive-helper", sourceFilesIn(t, prov, ansible), pat, func(path, line string) bool {
		return allowListPaths[filepath.Base(path)]
	})
}

func TestArchitectureGuard_noRunningStateSkipGate(t *testing.T) {
	t.Parallel()
	// Matches `state.Exists && state.Running` — the upgrade-hostile skip
	// pattern at the top of Provision(). Two legitimate uses are allow-listed:
	//   - base.go WaitForService: polls *until* running (inverse of skip)
	//   - proxy_guard.go: early-returns port-safety check when our own
	//     service is already listening (not a provision skip)
	prov, _ := repoSourceRoots(t)
	allowListPaths := map[string]bool{
		"base.go":        true,
		"proxy_guard.go": true,
	}
	pat := regexp.MustCompile(`state\.Exists\s*&&\s*state\.Running`)
	scanSources(t, "no-running-state-skip", sourceFilesIn(t, prov), pat, func(path, line string) bool {
		return allowListPaths[filepath.Base(path)]
	})
}

func TestArchitectureGuard_noCompatibilityNarrationInComments(t *testing.T) {
	t.Parallel()
	// docs/standards/code-comments.md bans history narration ("used to"),
	// roadmap ("Phase N", "will be replaced"), and stream-of-consciousness
	// intent ("Let's", "we can", "should probably"). Comments must state
	// local invariants; everything else belongs in commits or PRs.
	//
	// Applies to both the provisioner tree and the ansible task layer —
	// narration drift shows up first in whichever file is touched last.
	prov, ansible := repoSourceRoots(t)
	bannedSubstrings := []string{
		"kept for drift",
		"kept for callers",
		"kept for compat",
		"used to ",
		"will be replaced",
		"for backwards",
		"for backward",
		"Phase 1",
		"Phase 2",
		"Phase 3",
		"Phase 4",
		"Phase 5",
		"TODO(",
		"FIXME(",
	}
	bannedPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\b[Ll]et'?s\b`),
		regexp.MustCompile(`\b[Ww]e (can|could|should|might|want|may)\b`),
		regexp.MustCompile(`\bshould probably\b`),
		regexp.MustCompile(`\bmaybe\b`),
		regexp.MustCompile(`\bfor now\b`),
	}
	for _, path := range sourceFilesIn(t, prov, ansible) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trim := strings.TrimSpace(line)
			if !strings.HasPrefix(trim, "//") {
				continue
			}
			for _, phrase := range bannedSubstrings {
				if strings.Contains(line, phrase) {
					t.Errorf("[comment-standard] %s:%d narration %q: %s",
						filepath.Base(path), i+1, phrase, trim)
				}
			}
			for _, pat := range bannedPatterns {
				if pat.MatchString(line) {
					t.Errorf("[comment-standard] %s:%d narration matches %s: %s",
						filepath.Base(path), i+1, pat.String(), trim)
				}
			}
		}
	}
}
