package collector_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The agent must never read /proc/PID/cmdline or /proc/PID/environ.
//
// WHY THIS IS A TEST AND NOT A CODE REVIEW NOTE. `pid: host` gives the agent
// container a readable /proc entry for every process on the box. cmdline is
// argv, and argv routinely carries credentials: `mysql -p<pass>`,
// `-Dspring.datasource.password=...`, `--token=...`. environ is worse. An
// agent that shipped either to the hub would turn netra into a credential
// exfiltration path with a database behind it — and the operator who enabled
// `pid: host` for a CPU chart would have no idea.
//
// The trap is that the DANGEROUS implementation is the NATURAL one.
// /proc/PID/stat gives a 15-byte truncated `comm`, so the obvious way to get
// readable process names — "nginx: worker process", "/usr/bin/python3
// /opt/app/main.py" — is to read cmdline. Whoever writes the next collector
// that names a process will reach for it unless something stops them. This is
// that thing. netra shipped a per-process collector once and has since removed
// it, so no such reader exists today — which is exactly when a guard is worth
// keeping, not dropping.
//
// Process identity is `comm`, from /proc/PID/comm or field 2 of /proc/PID/stat.
// The cost is real and accepted: names truncate ("postgres: chec") and
// interpreted programs collapse to "python3" rather than the script they run.
// Aggregation is by name anyway, and the alternative is shipping secrets.
//
// The threat model is a well-meaning contributor, not an attacker. Anyone who
// wants to defeat this can; the point is that they cannot do it by accident.
const (
	// A line carrying this marker is exempt. It exists so the guard is never
	// the thing standing in someone's way at 2am — a genuine need becomes a
	// visible, greppable decision rather than a reason to delete the test.
	// With literal-only matching below, it should never be needed.
	allowMarker = "//netra:allow-proc-read"

	agentMain = "./cmd/netra-agent"
)

// forbidden matches the /proc file names inside a Go string literal.
//
// Three properties, each load-bearing:
//
//   - STRING LITERALS ONLY. A comment saying "never read cmdline" is fine, and
//     a collector that names processes will want to write exactly that. Only
//     string literal nodes are examined — see literalsOf.
//   - WORD BOUNDARIES. `environ` is a substring of "environment", which appears
//     in config.go's package comment. A naive substring match would be red the
//     moment this file landed.
//   - CASE-SENSITIVE LOWERCASE. os.Environ() reads the agent's OWN environment,
//     which is not the risk and stays allowed. The /proc file names are
//     lowercase.
var forbidden = regexp.MustCompile(`\b(cmdline|environ)\b`)

// rawLiterals is the FALLBACK extractor, used only when a file does not parse.
// It runs over the whole file rather than line by line: a backtick literal can
// span lines, and a per-line match would never see its interior — the guard
// would fail OPEN on exactly the shape a path-building const block takes.
var rawLiterals = regexp.MustCompile("\"[^\"\\n]*\"|`[^`]*`")

func TestAgentNeverReadsProcessArgsOrEnviron(t *testing.T) {
	var findings []string

	for _, root := range guardRoots(t) {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Test code does not ship. This guard file holds the words it bans.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, ferr := scanFile(path)
			if ferr != nil {
				return ferr
			}
			findings = append(findings, found...)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	if len(findings) == 0 {
		return
	}

	t.Errorf(`the agent must never read /proc/PID/cmdline or /proc/PID/environ:

%s

cmdline is argv and argv carries credentials — mysql -p<pass>,
-Dspring.datasource.password=..., --token=... The hub stores what it is sent,
and the operator who enabled 'pid: host' did it for a CPU chart.

Process identity comes from comm: /proc/PID/comm, or field 2 of /proc/PID/stat.
Yes, it truncates at 15 bytes and turns scripts into "python3". That is the
trade, and it was made deliberately (spec 6.2).

If you genuinely need one of these files, mark the line %s
so the decision is visible rather than silent.`,
		strings.Join(findings, "\n"), allowMarker)
}

// guardRoots is every directory in this module that the agent binary is built
// from, ASKED OF THE COMPILER rather than listed by hand.
//
// A hand-written list was wrong the day it was written: it named internal/agent
// and cmd/netra-agent, so internal/shared/gen/netra/v1 was never walked — and that is
// the tree that matters most. Adding `string cmdline = 5;` to any sample
// message and running `make proto` regenerates ingest.pb.go with a
// `protobuf:"...name=cmdline..."` struct tag, which is a string literal this
// guard would catch, in a file it never opened. The wire format is the whole
// point: a field that reaches the hub is the violation, and the proto comment
// claims this test prevents exactly that.
//
// `go list -deps` means any package a future import pulls into the agent is
// guarded automatically, with no list to keep in step.
func guardRoots(t *testing.T) []string {
	t.Helper()

	modRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}

	cmd := exec.Command("go", "list", "-deps", "-f", "{{.Dir}}", agentMain)
	cmd.Dir = modRoot
	out, err := cmd.Output()
	if err != nil {
		// Fails CLOSED. A guard that quietly scanned nothing because `go list`
		// broke would be worse than no guard: it would report success.
		t.Fatalf("go list -deps %s: %v", agentMain, err)
	}

	// ".." — all of internal/agent — UNCONDITIONALLY, alongside whatever the
	// compiler reports. `go list -deps` covers only what main already imports,
	// which narrows the guard in the other direction: a new collector written
	// before it is wired into netra-agent would be unguarded until the import
	// lands — precisely the window in which a collector that names processes
	// is being written, and precisely what this guard exists to constrain. Overlap with a computed root is harmless — WalkDir would visit
	// a file twice and report it twice, and duplicate findings in a failure
	// message cost nothing next to a missed one.
	roots := []string{".."}
	// Absolute form of that root, so the loop below can tell which computed
	// dirs it already covers.
	agentTree, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving the agent tree: %v", err)
	}

	for _, dir := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		// Only this module. The standard library and the module cache are not
		// ours to police, and scanning them would be both slow and noisy.
		rel, err := filepath.Rel(modRoot, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// Skip only what the ".." root already covers, so the common case does
		// not walk internal/agent twice on every run.
		if dir == agentTree || strings.HasPrefix(dir, agentTree+string(filepath.Separator)) {
			continue
		}
		roots = append(roots, dir)
	}

	// The agent imports at least its own collector package and the generated
	// wire types; a result this small means the query, not the program, changed.
	if len(roots) < 2 {
		t.Fatalf("go list returned %d in-module dirs for %s, expected the agent's own packages: %v",
			len(roots), agentMain, roots)
	}
	return roots
}

// scanFile reports every forbidden string literal in one file, as
// "path:line: text".
func scanFile(path string) ([]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	lits, ok := literalsOf(fset, path, src)
	if !ok {
		// Unparseable: fall back to the raw regexp. It over-reports (a comment
		// mentioning "cmdline" in quotes counts), which is the correct
		// direction to be wrong in — this must never MISS a violation because a
		// file did not compile.
		return scanRaw(path, src), nil
	}

	var out []string
	for _, lit := range lits {
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			// Not unquotable: test the raw token rather than skipping it.
			val = lit.Value
		}
		if !forbidden.MatchString(val) {
			continue
		}
		pos := fset.Position(lit.Pos())
		if lineHasMarker(src, pos.Line) {
			continue
		}
		out = append(out, fmt.Sprintf("  %s:%d: %s", path, pos.Line, strings.TrimSpace(lit.Value)))
	}
	return out, nil
}

// literalsOf returns every string literal node in the file.
//
// The AST is what makes "string literals only" true rather than aspirational.
// The previous regexp had no idea what a comment was, so
// `// names come from comm, never "cmdline"` failed the build — the exact
// comment this file predicts such a collector's author will write, in the
// repo's own backtick-quoting house style. It also could not see inside a
// multi-line raw string, so the same path hidden in one passed. Both directions
// were wrong; the parser gets both right.
//
// ast.Inspect reaches struct tags too, which is how generated protobuf files
// are covered.
func literalsOf(fset *token.FileSet, path string, src []byte) ([]*ast.BasicLit, bool) {
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	var lits []*ast.BasicLit
	ast.Inspect(file, func(n ast.Node) bool {
		if lit, isLit := n.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
			lits = append(lits, lit)
		}
		return true
	})
	return lits, true
}

func scanRaw(path string, src []byte) []string {
	var out []string
	for _, lit := range rawLiterals.FindAllIndex(src, -1) {
		if !forbidden.Match(src[lit[0]:lit[1]]) {
			continue
		}
		line := 1 + strings.Count(string(src[:lit[0]]), "\n")
		if lineHasMarker(src, line) {
			continue
		}
		out = append(out, fmt.Sprintf("  %s:%d: %s", path, line,
			strings.TrimSpace(string(src[lit[0]:lit[1]]))))
	}
	return out
}

// lineHasMarker reports whether the 1-based line carries the exemption marker.
func lineHasMarker(src []byte, line int) bool {
	lines := strings.Split(string(src), "\n")
	if line < 1 || line > len(lines) {
		return false
	}
	return strings.Contains(lines[line-1], allowMarker)
}

// TestGuardDetectsTheShapesItIsMeantTo pins the scanner's behaviour on the
// shapes a hand-rolled version got wrong. Each case is a real file on disk,
// because scanFile reads files.
//
// Without these, the guard's own properties are only assertable by editing the
// agent and watching CI — which is how the gaps survived review in the first
// place: the guard was green, and green proved nothing about what it covered.
func TestGuardDetectsTheShapesItIsMeantTo(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool // want at least one finding
	}{{
		name: "one-line literal",
		src:  "package p\n\nconst path = \"/proc/self/cmdline\"\n",
		want: true,
	}, {
		// The wire format. This is the shape `make proto` produces after a
		// cmdline field is added to the .proto, in a tree the old guardRoots
		// never walked.
		name: "generated protobuf struct tag",
		src: "package p\n\ntype S struct {\n" +
			"\tCmdline string `protobuf:\"bytes,5,opt,name=cmdline,proto3\" json:\"cmdline,omitempty\"`\n}\n",
		want: true,
	}, {
		// A per-line scan never sees the interior of one of these.
		name: "multi-line raw string",
		src:  "package p\n\nconst paths = `\n/proc/self/cmdline\n`\n",
		want: true,
	}, {
		// The comment this file predicts the collector's author will write.
		name: "comment naming the file in quotes",
		src:  "package p\n\n// never read \"cmdline\" — see spec 6.2\nfunc f() {}\n",
		want: false,
	}, {
		name: "comment naming the file in backticks",
		src:  "package p\n\n// names come from `comm`, never `cmdline`\nfunc f() {}\n",
		want: false,
	}, {
		// os.Environ reads the agent's OWN environment and stays allowed, as
		// does the word "environment" in prose.
		name: "os.Environ and the word environment",
		src:  "package p\n\n// the agent's own environment\nconst s = \"environment\"\n",
		want: false,
	}, {
		name: "explicit allow marker",
		src:  "package p\n\nconst path = \"/proc/self/cmdline\" //netra:allow-proc-read\n",
		want: false,
	}, {
		// Falls over to the raw regexp, which over-reports rather than missing.
		name: "unparseable file is still scanned",
		src:  "package p\n\nthis is not go\n\nconst x = \"/proc/self/cmdline\"\n",
		want: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probe.go")
			if err := os.WriteFile(path, []byte(tc.src), 0o600); err != nil {
				t.Fatalf("writing probe: %v", err)
			}
			got, err := scanFile(path)
			if err != nil {
				t.Fatalf("scanFile: %v", err)
			}
			if (len(got) > 0) != tc.want {
				t.Errorf("findings=%v, want any=%v\nsource:\n%s", got, tc.want, tc.src)
			}
		})
	}
}

// TestGuardRootsCoverTheWireFormat pins both coverage gaps at once.
//
// Membership is not the property worth asserting — a directory can be covered
// by an ANCESTOR root — so this asks whether each path is actually walked.
// Both directions matter and each was wrong once:
//
//   - internal/shared/gen/netra/v1 must be reachable, or a cmdline field added to the
//     proto passes green. It is not under internal/agent, so only the computed
//     roots can supply it.
//   - internal/agent must be reachable WHOLE, not just the packages main
//     already imports, or a collector is unguarded while it is being written.
func TestGuardRootsCoverTheWireFormat(t *testing.T) {
	roots := guardRoots(t)

	covered := func(rel string) bool {
		want, err := filepath.Abs(filepath.Join("..", "..", "..", rel))
		if err != nil {
			t.Fatalf("resolving %s: %v", rel, err)
		}
		for _, r := range roots {
			abs, err := filepath.Abs(r)
			if err != nil {
				continue
			}
			if want == abs || strings.HasPrefix(want, abs+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}

	if !covered(filepath.Join("internal", "shared", "gen", "netra", "v1")) {
		t.Error("internal/shared/gen/netra/v1 is not walked — a cmdline field added to the proto would pass")
	}
	if !covered(filepath.Join("internal", "agent", "collector")) {
		t.Error("internal/agent/collector is not walked")
	}
	// A package that exists but is not yet imported by main: the shape a new
	// collector has while it is being written.
	if !covered(filepath.Join("internal", "agent", "notyetimported")) {
		t.Error("an un-imported package under internal/agent is not walked — " +
			"a collector would be unguarded until it is wired into main")
	}
}
