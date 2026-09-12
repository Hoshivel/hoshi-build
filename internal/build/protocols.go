package build

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
)

// DescriptorProtocols is the release descriptor's `protocols` object
// (engineering/release.md §12): what this artifact serves, and what it calls.
//
// Both slices are non-nil so they marshal as `[]` rather than `null`. The
// difference matters to the reader: the standard says an absent `protocols`
// must be reported as undeclared rather than read as "no dependencies", so the
// object this tool writes has to be able to say "none" out loud.
type DescriptorProtocols struct {
	Provides []DescriptorProtocol `json:"provides"`
	Requires []DescriptorProtocol `json:"requires"`
}

// DescriptorProtocol is one entry of either list (§12.1).
//
// ContractRevision means two things depending on which list holds it — what
// this artifact implements, or the floor it needs from the other side. One key
// rather than two because the compatibility rule compares exactly these two
// values, and two names would make its two sides look like two things.
type DescriptorProtocol struct {
	Protocol         string `json:"protocol"`
	Version          int    `json:"version"`
	ContractRevision int    `json:"contract_revision"`
}

// protocolPackage is one linked package that declares a protocol.
type protocolPackage struct {
	importPath string
	name       string
	version    int
	revision   int
}

// The three package-scope constants a protocol implementation declares.
const (
	constProtocol = "Protocol"
	constName     = "ProtocolName"
	constRevision = "ContractRevision"
)

// linkedProtocols reads the protocols out of the packages this target actually
// links.
//
// The list comes from `go list -deps` on the same package, in the same
// directory, under the same GOOS/GOARCH as the build — so it is the build's own
// answer to "what went in", not a second opinion. Build constraints can make it
// differ between targets, which is why it is asked per target rather than once.
//
// Standard-library packages are dropped by `go list` itself. That is a cost
// filter, not a correctness one — no standard-library package declares these
// constants, and a test for it could not fail — but parsing several hundred
// packages of source on every build is the difference between a question worth
// asking per target and one that is not.
//
// What is load-bearing is that there is no filter *by module path* here. A list
// of where protocol implementations are allowed to live would be the same kind
// of second copy that the values themselves must not come from (release.md
// §12.2): it would silently drop a protocol that moved.
func linkedProtocols(ctx context.Context, r run.Runner, cfg *config.Config,
	t config.Target) ([]protocolPackage, error) {

	dir := filepath.Join(cfg.Root, filepath.FromSlash(cfg.Go.Dir))
	const format = `{{if not .Standard}}{{.ImportPath}}` + "\t" +
		`{{.Dir}}` + "\t" + `{{join .GoFiles ","}}{{end}}`
	out, err := r.Capture(ctx, run.Cmd{
		Dir:  dir,
		Env:  goEnv(t),
		Name: "go",
		Args: []string{"list", "-deps", "-mod=readonly", "-f", format, cfg.Go.Package},
	})
	if err != nil {
		return nil, fmt.Errorf("問不到 %s 連進去哪些套件：%w", cfg.Go.Package, err)
	}

	var found []protocolPackage
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 3 || fields[1] == "" || fields[2] == "" {
			continue
		}
		pkg, err := readProtocol(fields[0], fields[1], strings.Split(fields[2], ","))
		if err != nil {
			return nil, err
		}
		if pkg != nil {
			found = append(found, *pkg)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	return found, nil
}

// readProtocol returns the protocol a package declares, or nil if it declares
// none.
//
// A package counts when it declares both `Protocol` and `ProtocolName`. Both,
// because either alone is unusable: a version with no name cannot be matched
// against anything, and a name with no version cannot be compared.
// `ContractRevision` is optional and absent reads as 0, which is the value the
// versioning standard §8.2 already gives a peer that predates the ledger — it
// satisfies no requirement of 1 or more.
func readProtocol(importPath, dir string, files []string) (*protocolPackage, error) {
	pkg := protocolPackage{importPath: importPath}
	fset := token.NewFileSet()
	for _, name := range files {
		if name == "" {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil,
			parser.SkipObjectResolution)
		if err != nil {
			// A dependency this tool cannot parse is not this build's problem
			// as long as the compiler took it: skip the file rather than fail
			// the build over a syntax this parser version does not know.
			continue
		}
		if err := scanConsts(file, importPath, &pkg); err != nil {
			return nil, err
		}
	}
	if pkg.name == "" || pkg.version == 0 {
		return nil, nil
	}
	return &pkg, nil
}

// scanConsts pulls the three constants out of one file's package-scope decls.
//
// Only a plain literal is accepted. Anything else — a computed expression, an
// iota, a reference to another package — is refused rather than guessed at,
// because a wrong protocol version in a descriptor is not visible until a
// caller is gated against it.
func scanConsts(file *ast.File, importPath string, pkg *protocolPackage) error {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			name := value.Names[0].Name
			if name != constProtocol && name != constName && name != constRevision {
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok {
				return fmt.Errorf(
					"%s 的 `%s` 不是字面值，讀不出編進去的是什麼。"+
						"協定的名字、版本與 revision 必須是常數字面值"+
						"（發佈標準 §12.2）", importPath, name)
			}
			if err := assign(lit, name, importPath, pkg); err != nil {
				return err
			}
		}
	}
	return nil
}

func assign(lit *ast.BasicLit, name, importPath string, pkg *protocolPackage) error {
	switch name {
	case constName:
		if lit.Kind != token.STRING {
			return fmt.Errorf("%s 的 `%s` 不是字串", importPath, name)
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return fmt.Errorf("%s 的 `%s` 解不開：%w", importPath, name, err)
		}
		pkg.name = text
	case constProtocol, constRevision:
		if lit.Kind != token.INT {
			return fmt.Errorf("%s 的 `%s` 不是整數", importPath, name)
		}
		n, err := strconv.Atoi(lit.Value)
		if err != nil {
			return fmt.Errorf("%s 的 `%s` 解不開：%w", importPath, name, err)
		}
		if name == constProtocol {
			pkg.version = n
		} else {
			pkg.revision = n
		}
	}
	return nil
}

// describeProtocols splits the linked protocols into the two lists.
//
// What the service serves is declared in the config, because "which protocol am
// I the server of" is not something the linked code says: hoshi-data and every
// caller of it link the same package. Everything else linked is something this
// artifact calls.
//
// A declared name with no matching linked package is refused. The alternative
// would be writing a revision of 0 for it, and 0 in `provides` means "satisfies
// no requirement", so every caller's gate would fail on a service that is
// actually fine. Refusing says which of the two mistakes it is.
func describeProtocols(cfg *config.Config, linked []protocolPackage) (*DescriptorProtocols, error) {
	serves := make(map[string]bool, len(cfg.Protocols.Provides))
	for _, name := range cfg.Protocols.Provides {
		serves[name] = true
	}

	out := &DescriptorProtocols{
		Provides: []DescriptorProtocol{},
		Requires: []DescriptorProtocol{},
	}
	seen := make(map[string]bool, len(linked))
	for _, pkg := range linked {
		seen[pkg.name] = true
		entry := DescriptorProtocol{
			Protocol:         pkg.name,
			Version:          pkg.version,
			ContractRevision: pkg.revision,
		}
		if serves[pkg.name] {
			out.Provides = append(out.Provides, entry)
			continue
		}
		out.Requires = append(out.Requires, entry)
	}

	var missing []string
	for _, name := range cfg.Protocols.Provides {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf(
			"`protocols.provides` 宣告了 %s，但產物裡沒有任何套件宣告這個協定。"+
				"版本與 revision 只能從編進去的實作讀出來（發佈標準 §12.2），"+
				"所以這裡沒有可以寫的值——不是寫 0：`provides` 的 0 代表"+
				"「滿足不了任何要求」，會讓每一個呼叫端的閘都紅",
			strings.Join(missing, "、"))
	}
	return out, nil
}

// buildProtocols answers the protocol question once per distinct target.
//
// Only a Go build has an answer: the question is which implementation the
// artifact linked, and an npm bundle links none. A `go-npm` repo's artifact is
// the directory holding both the executable and `web/`, so it is a Go artifact
// with a target like any other and gets the same object.
//
// An artifact with no target is skipped rather than asked about under an
// invented platform. `go list` needs a real GOOS/GOARCH, and answering for a
// platform this artifact was not built for would be a plausible-looking wrong
// value in a field whose whole job is to be checkable.
func (b *builder) buildProtocols(ctx context.Context, result *Result) (
	map[config.Target]*DescriptorProtocols, error) {

	if !b.cfg.BuildsGo() {
		return nil, nil
	}
	out := map[config.Target]*DescriptorProtocols{}
	for i := range result.Artifacts {
		target := result.Artifacts[i].Target
		if target == (config.Target{}) {
			continue
		}
		if _, done := out[target]; done {
			continue
		}
		linked, err := linkedProtocols(ctx, b.runner, b.cfg, target)
		if err != nil {
			return nil, err
		}
		declared, err := describeProtocols(b.cfg, linked)
		if err != nil {
			return nil, err
		}
		out[target] = declared
	}
	return out, nil
}
