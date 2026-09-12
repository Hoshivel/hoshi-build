package build

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
)

// A module whose main package links two protocol implementations and one
// ordinary package. Written as source rather than mocked because the thing
// under test is "what did the artifact actually link", and a mock answers that
// question by being told.
func protocolFixture(extra map[string]string) map[string]string {
	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.24\n",
		"cmd/fixture/main.go": "package main\n\n" +
			"import (\n" +
			"\t_ \"fixture/internal/served\"\n" +
			"\t_ \"fixture/internal/called\"\n" +
			"\t_ \"fixture/internal/plain\"\n" +
			")\n\nfunc main() {}\n",
		"internal/served/served.go": "package served\n\n" +
			"const Protocol = 1\n" +
			"const ProtocolName = \"demo-store\"\n" +
			"const ContractRevision = 7\n",
		// No ContractRevision: a protocol that has no revision ledger reads as
		// 0, which is the value the versioning standard already gives a peer
		// that predates the ledger.
		"internal/called/called.go": "package called\n\n" +
			"const Protocol = 2\n" +
			"const ProtocolName = \"demo-directory\"\n",
		"internal/plain/plain.go": "package plain\n\nconst Answer = 42\n",
	}
	for name, body := range extra {
		files[name] = body
	}
	return files
}

func requireToolchain(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("環境裡沒有 go")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
}

func buildFixture(t *testing.T, yaml string, files map[string]string) (*Result, string, error) {
	t.Helper()
	root := testRepo(t, yaml, files)
	cfg := loadRepo(t, root)
	res, err := Run(context.Background(), cfg, quietPrinter(), run.NewExecRunner(),
		Options{ToolVersion: "v0.9.0"})
	return res, root, err
}

const fixtureYAML = "name: fixture\ntype: go\noutput: dist/\ntargets:\n  - %s/%s\n" +
	"protocols:\n  provides:\n    - demo-store\n"

func fixtureConfig() string {
	return strings.Replace(
		strings.Replace(fixtureYAML, "%s", runtime.GOOS, 1), "%s", runtime.GOARCH, 1)
}

// The end-to-end proof: a real build, the real toolchain, and the two lists
// read out of the packages that actually went in.
//
// Everything about this field can be wrong while the build succeeds, so the
// assertion has to be on the values, not the shape.
func TestDescriptorReportsTheProtocolsTheArtifactLinked(t *testing.T) {
	requireToolchain(t)

	res, root, err := buildFixture(t, fixtureConfig(), protocolFixture(nil))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	_ = root

	got := readDescriptor(t, res.Artifacts[0].Descriptor)
	protocols, ok := got["protocols"].(map[string]any)
	if !ok {
		t.Fatalf("描述子沒有 protocols：%v", got)
	}

	provides := protocolNames(t, protocols["provides"])
	requires := protocolNames(t, protocols["requires"])
	if len(provides) != 1 || provides[0] != "demo-store" {
		t.Errorf("provides = %v，want [demo-store]", provides)
	}
	if len(requires) != 1 || requires[0] != "demo-directory" {
		t.Errorf("requires = %v，want [demo-directory]——"+
			"宣告提供的那一個要在 provides，其餘連進去的都是 requires", requires)
	}

	entry := protocols["provides"].([]any)[0].(map[string]any)
	if entry["version"].(float64) != 1 || entry["contract_revision"].(float64) != 7 {
		t.Errorf("provides[0] = %v，want version 1、contract_revision 7——"+
			"版本與 revision 必須是編進去的那兩個常數", entry)
	}
	called := protocols["requires"].([]any)[0].(map[string]any)
	if called["version"].(float64) != 2 || called["contract_revision"].(float64) != 0 {
		t.Errorf("requires[0] = %v，want version 2、contract_revision 0——"+
			"沒有 revision 帳本的協定讀成 0，不是省略", called)
	}
}

func protocolNames(t *testing.T, value any) []string {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("不是陣列：%v", value)
	}
	var names []string
	for _, item := range list {
		names = append(names, item.(map[string]any)["protocol"].(string))
	}
	return names
}

// A package that does not declare a protocol must not appear. Otherwise every
// dependency in the module graph becomes a line in the descriptor and the two
// lists stop meaning anything.
func TestOrdinaryPackagesAreNotProtocols(t *testing.T) {
	requireToolchain(t)

	res, _, err := buildFixture(t, fixtureConfig(), protocolFixture(nil))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	body, err := os.ReadFile(res.Artifacts[0].Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "fixture/internal/plain") ||
		strings.Contains(string(body), "\"42\"") {
		t.Errorf("沒有宣告協定的套件進了描述子：%s", body)
	}
}

// Declaring a protocol the artifact does not carry is refused rather than
// written as revision 0. Zero in `provides` means "satisfies no requirement",
// so it would turn a healthy service into a red gate for every caller.
func TestProvidesWithoutAnImplementationIsRefused(t *testing.T) {
	requireToolchain(t)

	yaml := strings.Replace(fixtureConfig(), "- demo-store", "- demo-store\n    - demo-absent", 1)
	_, _, err := buildFixture(t, yaml, protocolFixture(nil))
	if err == nil {
		t.Fatal("宣告了產物裡沒有的協定卻通過了")
	}
	if !strings.Contains(err.Error(), "demo-absent") {
		t.Errorf("錯誤沒有指名是哪一個協定：%v", err)
	}
}

// A build that declares nothing still writes the object, with two empty lists.
// Absent and empty are different answers — the standard requires the deployment
// layer to refuse an absent one rather than read it as "no dependencies" — so
// the tool must be able to say "none" out loud.
func TestNoDeclarationStillSaysNoneOutLoud(t *testing.T) {
	requireToolchain(t)

	yaml := "name: fixture\ntype: go\noutput: dist/\ntargets:\n  - " +
		runtime.GOOS + "/" + runtime.GOARCH + "\n"
	files := map[string]string{
		"go.mod":              "module fixture\n\ngo 1.24\n",
		"cmd/fixture/main.go": "package main\n\nfunc main() {}\n",
	}
	res, _, err := buildFixture(t, yaml, files)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	body, err := os.ReadFile(res.Artifacts[0].Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Protocols *DescriptorProtocols `json:"protocols"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Protocols == nil {
		t.Fatal("protocols 整個不見了——那代表「未宣告」，而這次是「沒有相依」")
	}
	if got.Protocols.Provides == nil || got.Protocols.Requires == nil {
		t.Errorf("兩個陣列必須是 []，不是 null：%s", body)
	}
	if len(got.Protocols.Provides) != 0 || len(got.Protocols.Requires) != 0 {
		t.Errorf("憑空多出相依：%+v", got.Protocols)
	}
}

// A constant that is not a plain literal cannot be read out of the source, and
// guessing would put a wrong protocol version in a field whose only job is to
// be compared.
func TestAComputedConstantIsRefusedRatherThanGuessed(t *testing.T) {
	dir := t.TempDir()
	source := "package served\n\nconst base = 1\n\nconst Protocol = base\n" +
		"const ProtocolName = \"demo-store\"\n"
	if err := os.WriteFile(filepath.Join(dir, "served.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readProtocol("fixture/internal/served", dir, []string{"served.go"})
	if err == nil {
		t.Fatal("算出來的常數被當成字面值讀走了")
	}
	if !strings.Contains(err.Error(), "Protocol") {
		t.Errorf("錯誤沒有指名是哪一個常數：%v", err)
	}
}

// Version and name are both needed: a version with no name matches nothing, a
// name with no version compares against nothing. Either alone is not a protocol
// this tool can write down.
func TestHalfADeclarationIsNotAProtocol(t *testing.T) {
	cases := map[string]string{
		"只有版本": "package p\n\nconst Protocol = 1\n",
		"只有名字": "package p\n\nconst ProtocolName = \"demo\"\n",
	}
	for name, source := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readProtocol("fixture/p", dir, []string{"p.go"})
		if err != nil {
			t.Fatalf("%s：readProtocol() error = %v", name, err)
		}
		if got != nil {
			t.Errorf("%s 卻被當成一個協定：%+v", name, got)
		}
	}
}

// The config file holds names only. A version there is a second copy that can
// forget to move, and it forgets in the dangerous direction.
func TestTheConfigDeclaresNamesWithoutVersions(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\nprotocols:\n"+
		"  provides:\n    - demo-store\n  version: 3\n", nil)
	if _, err := config.LoadFrom(root); err == nil {
		t.Fatal("`protocols.version` 被收下了")
	}
}

// A repo with no Go artifact has nothing to read the values out of, so
// declaring what it serves is a claim nothing can back.
func TestAnNpmRepoCannotDeclareProtocols(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: npm\nprotocols:\n  provides:\n    - demo-store\n", nil)
	_, err := config.LoadFrom(root)
	if err == nil {
		t.Fatal("type: npm 宣告了 provides 卻通過了")
	}
	if !strings.Contains(err.Error(), "provides") {
		t.Errorf("錯誤沒有指向那個鍵：%v", err)
	}
}

func TestTheSameProtocolTwiceIsRefused(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\nprotocols:\n"+
		"  provides:\n    - demo-store\n    - demo-store\n", nil)
	if _, err := config.LoadFrom(root); err == nil {
		t.Fatal("同一個協定列兩次卻通過了")
	}
}
