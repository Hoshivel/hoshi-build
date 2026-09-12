package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
)

// DescriptorSchema is the version of the release descriptor this tool writes.
const DescriptorSchema = "hoshi.release/v1"

// DescriptorSuffix is appended to an artifact's name to get its descriptor.
const DescriptorSuffix = ".release.json"

// now is the clock, replaced in tests.
var now = time.Now

// Descriptor is the release descriptor defined by the organisation's
// engineering/release.md.
//
// # Why a file beside the artifact rather than something inside it
//
// An executable can print a version string, but that string was stamped in at
// build time and changing a byte of the binary does not change it. A static
// bundle has no such string at all, and a directory artifact is not a file. All
// three need a fact that lives outside the bytes, can be recomputed
// independently, and travels with them.
//
// # Why the build tool does not fill in everything
//
// The standard splits the descriptor between two owners. This tool writes the
// build fields; the deployment tool adds `deployment` when it binds the
// artifact to a node. The configuration digest belongs to that second half
// because there is no configuration to hash here: a node's service
// configuration is rendered by the deployment layer, and it does not exist
// until after the artifact has left this machine.
type Descriptor struct {
	Schema   string             `json:"schema"`
	Service  string             `json:"service"`
	Type     string             `json:"type"`
	Version  string             `json:"version"`
	Commit   string             `json:"commit"`
	Dirty    bool               `json:"dirty"`
	BuiltAt  string             `json:"built_at"`
	Target   *DescriptorTarget  `json:"target,omitempty"`
	Artifact DescriptorArtifact `json:"artifact"`
	Builder  DescriptorBuilder  `json:"builder"`
	// Protocols is omitted when this build could not answer the question at
	// all. That is not the same as "no dependencies", and the standard
	// requires the deployment layer to keep the two apart: an absent object
	// must be reported as undeclared, because reading it as "requires
	// nothing" lets every gate pass.
	Protocols *DescriptorProtocols `json:"protocols,omitempty"`
}

type DescriptorTarget struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type DescriptorArtifact struct {
	Name string `json:"name"`
	// Kind is "file" or "dir".
	Kind string `json:"kind"`
	// SHA256 is the full 64 hex digits. Truncation is for display only: the
	// platform's release directories are named after the first 12, and 12 is
	// enough for a name but not for deciding whether two builds are the same.
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Files  int    `json:"files"`
}

type DescriptorBuilder struct {
	Tool        string `json:"tool"`
	ToolVersion string `json:"tool_version"`
	GoVersion   string `json:"go_version,omitempty"`
}

// gitFacts reports the commit an artifact came from and whether the tree that
// produced it had uncommitted changes.
//
// A commit that cannot be resolved is reported as empty rather than guessed:
// the descriptor's whole job is to be the fact nobody has to remember, so a
// plausible-looking wrong value is worse than an absent one.
//
// A tree whose cleanliness cannot be established is reported dirty. The
// standard forbids shipping a dirty artifact into a production wave, and the
// safe direction for "cannot tell" is the one that stops.
func gitFacts(ctx context.Context, r run.Runner, root string) (string, bool) {
	if _, err := r.Look("git"); err != nil {
		return "", false
	}
	commit, err := r.Capture(ctx, run.Cmd{
		Dir: root, Name: "git", Args: []string{"rev-parse", "HEAD"}})
	commit = strings.TrimSpace(commit)
	if err != nil || !isFullCommit(commit) {
		return "", false
	}
	status, err := r.Capture(ctx, run.Cmd{
		Dir: root, Name: "git", Args: []string{"status", "--porcelain"}})
	if err != nil {
		return commit, true
	}
	return commit, strings.TrimSpace(status) != ""
}

func isFullCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// goVersion asks the toolchain what it is, for the provenance fields.
func goVersion(ctx context.Context, r run.Runner) string {
	if _, err := r.Look("go"); err != nil {
		return ""
	}
	out, err := r.Capture(ctx, run.Cmd{Name: "go", Args: []string{"env", "GOVERSION"}})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// artifactDigest implements engineering/release.md §4.1.
//
// A single file hashes its own bytes. A directory hashes one line per regular
// file — `<sha256>  ./<relative path>\n` — ordered by the byte value of the
// relative path, and then hashes the concatenation.
//
// That is not an arbitrary choice: it reproduces, byte for byte, the shell
// pipeline hoshi-deploy already uses to name a release directory
// (`find -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum`). The
// first 12 digits of what this returns are therefore the directory name a node
// already has, so descriptors can land without renaming anything. A different
// rule — sorting differently, including directories, using a separator of its
// own — would read as a mismatch on every artifact from the first day.
func artifactDigest(root string) (sha string, total int64, files int, err error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", 0, 0, err
	}
	if !info.IsDir() {
		body, err := os.ReadFile(root)
		if err != nil {
			return "", 0, 0, err
		}
		return sha256Hex(body), int64(len(body)), 1, nil
	}

	type entry struct{ rel, digest string }
	var entries []entry
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Directories, symlinks and anything else that is not a regular file
		// are outside the rule, the same way `find -type f` leaves them out.
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		total += int64(len(body))
		entries = append(entries, entry{filepath.ToSlash(rel), sha256Hex(body)})
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	var stream strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&stream, "%s  ./%s\n", e.digest, e.rel)
	}
	return sha256Hex([]byte(stream.String())), total, len(entries), nil
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// describe builds one artifact's descriptor.
func (b *builder) describe(art *Artifact, version string, facts buildFacts) (*Descriptor, error) {
	digest, total, files, err := artifactDigest(art.Path)
	if err != nil {
		return nil, err
	}
	kind := "file"
	if art.IsDir {
		kind = "dir"
	}
	d := &Descriptor{
		Schema:  DescriptorSchema,
		Service: b.cfg.Name,
		Type:    b.cfg.Type,
		Version: version,
		Commit:  facts.commit,
		Dirty:   facts.dirty,
		BuiltAt: now().UTC().Format("2006-01-02T15:04:05Z"),
		Artifact: DescriptorArtifact{
			Name:   filepath.Base(art.Path),
			Kind:   kind,
			SHA256: digest,
			Bytes:  total,
			Files:  files,
		},
		Builder: DescriptorBuilder{
			Tool:        "hoshi-build",
			ToolVersion: facts.toolVersion,
			GoVersion:   facts.goVersion,
		},
	}
	if art.Target != (config.Target{}) {
		d.Target = &DescriptorTarget{OS: art.Target.OS, Arch: art.Target.Arch}
	}
	d.Protocols = facts.protocols[art.Target]
	return d, nil
}

// buildFacts are the things that are the same for every artifact in one build.
type buildFacts struct {
	commit      string
	dirty       bool
	goVersion   string
	toolVersion string
	// protocols is per target: build constraints can put different packages
	// into different platforms, so the question is asked once per target
	// rather than once per build.
	protocols map[config.Target]*DescriptorProtocols
}

// writeDescriptors writes one descriptor beside each artifact.
//
// Beside, never inside: a directory artifact's descriptor holds that
// directory's hash, so putting the file in there would change the thing it
// records.
//
// `type: npm` produces no descriptor. Its artifact *is* the output directory,
// so there is no "beside it" that is not the repository root — and the static
// sites are published through Cloudflare Pages rather than pushed to a node, so
// nothing downstream is waiting for one. engineering/release.md §7 says the
// same, rather than leaving the gap for someone to fill differently.
func (b *builder) writeDescriptors(ctx context.Context, result *Result, toolVersion string) error {
	if b.cfg.Type == config.TypeNpm {
		b.ui.Note("type: npm 的產物就是輸出目錄本身，沒有旁邊可以放描述子（發佈標準 §7）")
		return nil
	}
	if len(result.Artifacts) == 0 {
		return nil
	}

	commit, dirty := gitFacts(ctx, b.runner, b.cfg.Root)
	facts := buildFacts{
		commit:      commit,
		dirty:       dirty,
		goVersion:   goVersion(ctx, b.runner),
		toolVersion: toolVersion,
	}
	protocols, err := b.buildProtocols(ctx, result)
	if err != nil {
		return err
	}
	facts.protocols = protocols

	b.ui.Title("發佈描述子")
	for i := range result.Artifacts {
		art := &result.Artifacts[i]
		d, err := b.describe(art, result.Version, facts)
		if err != nil {
			return err
		}
		body, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return err
		}
		path := art.Path + DescriptorSuffix
		if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
			return err
		}
		art.Descriptor = path

		// Twelve digits is what an operator reads; the file keeps all 64.
		b.ui.OK("%s（%s…）", filepath.Base(path), d.Artifact.SHA256[:12])
	}
	if facts.commit == "" {
		b.ui.Warn("認不出 commit——描述子的 commit 是空的，這一份不能放進正式波次")
	} else if facts.dirty {
		b.ui.Warn("工作樹有未提交的變更——描述子標記 dirty，這一份重現不出來")
	}
	return nil
}
