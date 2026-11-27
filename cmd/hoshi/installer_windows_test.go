//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Execute the exact README block against fake HTTP and a tiny native fixture.
// The fixture's env action writes only a temp marker, never HKCU\Environment.
func TestWindowsREADMEInstaller(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(readme), "\r\n", "\n")
	match := regexp.MustCompile("(?s)<!-- hoshi-windows-install:start -->\\s*```powershell\\n(.*?)\\n```\\s*<!-- hoshi-windows-install:end -->").FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatal("README Windows installation block is missing")
	}
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	fixtureDir := t.TempDir()
	fixture := filepath.Join(fixtureDir, "fixture.exe")
	source := filepath.Join(fixtureDir, "main.go")
	const program = `package main
import("fmt";"os")
func main(){a:=os.Args[1:];if len(a)==3&&a[0]=="env"&&a[1]=="path"{if a[2]=="--help"{if os.Getenv("HOSHI_INSTALL_TEST_OLD")=="1"{os.Exit(3)};return};if a[2]=="add"{if os.WriteFile(os.Getenv("HOSHI_INSTALL_TEST_REPORT"),[]byte(os.Args[0]),0600)!=nil{os.Exit(4)};return}};if len(a)==1&&a[0]=="version"{fmt.Println("hoshi installer-fixture");return};os.Exit(5)}`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", fixture, source).CombinedOutput(); err != nil {
		t.Fatalf("build installer fixture: %v\n%s", err, output)
	}
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	psQuote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	for _, tc := range []struct {
		name                                          string
		existing, badHash, failedDownload, oldVersion bool
	}{
		{name: "fresh-install"},
		{name: "replace-existing", existing: true},
		{name: "hash-mismatch-preserves-existing", existing: true, badHash: true},
		{name: "interrupted-download-preserves-existing", existing: true, failedDownload: true},
		{name: "old-env-interface-preserves-existing", existing: true, oldVersion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			local := filepath.Join(root, "Local App's Data 測試")
			bin := filepath.Join(local, "Programs", "hoshi", "bin")
			exe := filepath.Join(bin, "hoshi.exe")
			report := filepath.Join(root, "env-action")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			old := []byte("existing installation must survive failed downloads")
			if tc.existing {
				if err := os.WriteFile(exe, old, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			expected := digest
			if tc.badHash {
				expected = strings.Repeat("0", 64)
			}
			stub := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
# Load the real utility module before replacing its HTTP cmdlets. Otherwise
# the mocks can suppress its normal autoload, hiding Get-FileHash in PS 5.1.
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1')
function Invoke-RestMethod {
  param([string]$Uri)
  if ($Uri -like '*api.github.com*') {
    return [pscustomobject]@{ assets = @(
      [pscustomobject]@{ name='hoshi-windows-amd64.exe'; browser_download_url='https://fixture.invalid/binary' },
      [pscustomobject]@{ name='hoshi-windows-amd64.exe.release.json'; browser_download_url='https://fixture.invalid/descriptor' }
    ) }
  }
  return [pscustomobject]@{ artifact = [pscustomobject]@{ sha256=%s } }
}
function Invoke-WebRequest {
  param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
  if ($env:HOSHI_INSTALL_TEST_FAIL_DOWNLOAD -eq '1') {
    [IO.File]::WriteAllText($OutFile, 'interrupted bytes')
    throw 'fixture network interruption'
  }
  Copy-Item -LiteralPath %s -Destination $OutFile
}
`, psQuote(expected), psQuote(fixture))
			post := fmt.Sprintf("\nif (($env:Path -split ';')[0] -ne %s) { throw 'current terminal PATH was not updated' }\n", psQuote(bin))
			command := exec.CommandContext(ctx, shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", stub+match[1]+post)
			command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			command.Env = append(os.Environ(), "LOCALAPPDATA="+local, "HOSHI_INSTALL_TEST_REPORT="+report,
				fmt.Sprintf("HOSHI_INSTALL_TEST_OLD=%d", boolInt(tc.oldVersion)), fmt.Sprintf("HOSHI_INSTALL_TEST_FAIL_DOWNLOAD=%d", boolInt(tc.failedDownload)))
			output, runErr := command.CombinedOutput()
			wantFailure := tc.badHash || tc.failedDownload || tc.oldVersion
			if (runErr != nil) != wantFailure {
				t.Fatalf("error=%v, expected failure=%v\n%s", runErr, wantFailure, output)
			}
			if wantFailure {
				fragment := "env path"
				if tc.badHash {
					fragment = "SHA-256"
				}
				if tc.failedDownload {
					fragment = "fixture network interruption"
				}
				if !strings.Contains(string(output), fragment) {
					t.Fatalf("failed for the wrong reason, want %q: %s", fragment, output)
				}
			}
			got, readErr := os.ReadFile(exe)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if wantFailure {
				if string(got) != string(old) {
					t.Fatal("failed installation replaced existing binary")
				}
				if _, err := os.Stat(report); !os.IsNotExist(err) {
					t.Fatal("failed installation invoked env add")
				}
			} else {
				if fmt.Sprintf("%x", sha256.Sum256(got)) != digest {
					t.Fatal("installed binary does not match descriptor")
				}
				called, err := os.ReadFile(report)
				if err != nil || !strings.EqualFold(string(called), exe) {
					t.Fatalf("env action marker=%q error=%v", called, err)
				}
				if !strings.Contains(string(output), "hoshi installer-fixture") {
					t.Fatalf("installed version did not run: %s", output)
				}
			}
			left, err := filepath.Glob(filepath.Join(bin, "hoshi-download-*.exe"))
			if err != nil || len(left) != 0 {
				t.Fatalf("download leftovers=%v error=%v", left, err)
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
