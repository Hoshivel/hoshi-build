#!/usr/bin/env python3
"""以本機合成 tarball 實走 npm 安裝輸入；不連 registry，不修改既有專案。"""

from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile


def write_json(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def fixture(root: Path) -> None:
    root.mkdir(parents=True)
    (root / "packages").mkdir()
    for label, version in (("a", "1.0.0"), ("b", "2.0.0")):
        archive = root / "packages" / f"compiler-{label}.tgz"
        files = {
            "package/package.json": json.dumps({
                "name": "fixture-compiler", "version": version, "main": "index.cjs",
            }),
            "package/index.cjs": (
                "const fs = require('node:fs'); "
                "fs.mkdirSync('dist', {recursive: true}); "
                f"fs.writeFileSync('dist/compiler.txt', '{version}'); "
                f"console.log('fixture compiler {version}');"
            ),
        }
        with tarfile.open(archive, "w:gz") as tar:
            for name, text in files.items():
                data = text.encode("utf-8")
                info = tarfile.TarInfo(name)
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        integrity = base64.b64encode(hashlib.sha512(archive.read_bytes()).digest()).decode()
        write_json(root / f"lock-{label}.json", {
            "name": "fixture-site", "version": "1.0.0", "lockfileVersion": 3, "requires": True,
            "packages": {
                "": {"name": "fixture-site", "version": "1.0.0",
                     "dependencies": {"fixture-compiler": "*"}},
                "node_modules/fixture-compiler": {
                    "version": version, "resolved": f"file:packages/compiler-{label}.tgz",
                    "integrity": "sha512-" + integrity,
                },
            },
        })
    write_json(root / "package.json", {
        "name": "fixture-site", "version": "1.0.0", "private": True,
        "scripts": {"build": "node -e \"require('fixture-compiler')\""},
        "dependencies": {"fixture-compiler": "*"},
    })
    (root / ".hoshi-build.yaml").write_text("name: fixture-site\ntype: npm\noutput: dist/\n")
    (root / ".npmrc").write_text("audit=false\nfund=false\nupdate-notifier=false\noffline=true\n")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--hoshi", type=Path, required=True, help="要驗證的 hoshi 二進位檔")
    parser.add_argument("--output", type=Path, required=True, help="尚不存在的隔離產物目錄")
    parser.add_argument("--expect-stale", action="store_true", help="只驗舊版 A→B 缺陷確實重現")
    args = parser.parse_args()
    binary, output = args.hoshi.resolve(), args.output.resolve()
    if not binary.is_file():
        parser.error("--hoshi 必須指向既有執行檔")
    if output.exists():
        parser.error("--output 已存在，拒絕覆蓋")
    output.mkdir(parents=True)
    root = output / "site"
    fixture(root)
    steps: list[dict[str, object]] = []

    def lock(label: str, name: str = "package-lock.json") -> None:
        (root / name).write_bytes((root / f"lock-{label}.json").read_bytes())

    def invoke(label: str, version: str, *, reused: bool = False, failure: bool = False,
               command: str = "build", install: str | None = None,
               record: bool | None = None) -> None:
        proc = subprocess.run([str(binary), command], cwd=root, capture_output=True,
                              text=True, encoding="utf-8", errors="replace", timeout=120)
        log = proc.stdout + proc.stderr
        (output / f"{label}.log").write_text(log, encoding="utf-8")
        package = root / "node_modules/fixture-compiler/package.json"
        installed = json.loads(package.read_text(encoding="utf-8"))["version"] if package.exists() else None
        artifact = root / "dist/compiler.txt"
        emitted = artifact.read_text() if artifact.exists() else None
        marker = root / "node_modules/.hoshi-install.json"
        step = {"case": label, "command": command, "exit": proc.returncode,
                "installed": installed, "output": emitted,
                "reused": "npm 安裝輸入相符" in log, "record": marker.is_file()}
        steps.append(step)
        write_json(output / "receipt.json", {"steps": steps})
        assert (proc.returncode != 0) == failure, (step, log)
        assert installed == version, (step, log)
        if command != "setup":
            assert emitted == version, (step, log)
        assert step["reused"] == reused, (step, log)
        if failure:
            assert "→ npm run build" not in log, log
        if install:
            assert "→ npm " + install in log, log
        if record is not None:
            assert step["record"] == record, step
        print(json.dumps(step, ensure_ascii=False))

    lock("a")
    invoke("install-a", "1.0.0", install="ci")
    lock("b")
    if args.expect_stale:
        invoke("stale-after-lock-b", "1.0.0")
        print("已重現：lock B 仍執行編譯器 A")
        return
    invoke("install-b", "2.0.0", install="ci", record=True)
    invoke("reuse-b", "2.0.0", reused=True)
    invoke("test-reuses-build-install", "2.0.0", reused=True, command="test")
    (root / "node_modules/.hoshi-install.json").write_text("{")
    invoke("corrupt-record", "2.0.0", install="ci", record=True)

    package_bytes = (root / "package.json").read_bytes()
    manifest = json.loads(package_bytes)
    manifest["dependencies"]["fixture-compiler"] = "999.0.0"
    write_json(root / "package.json", manifest)
    invoke("mismatched-package-fails", "2.0.0", failure=True, record=False)
    (root / "package.json").write_bytes(package_bytes)
    invoke("retry-after-failure", "2.0.0", install="ci", record=True)

    (root / "package-lock.json").unlink()
    lock("a", "npm-shrinkwrap.json")
    invoke("shrinkwrap-only", "1.0.0", install="ci")
    lock("b")
    invoke("shrinkwrap-beats-package-lock", "1.0.0", reused=True)
    lock("b", "npm-shrinkwrap.json")
    invoke("changed-shrinkwrap", "2.0.0", install="ci")

    config = root / ".hoshi-build.yaml"
    base_config = config.read_text()
    config.write_text(base_config + "npm:\n  install: always\n")
    invoke("always-first", "2.0.0", install="ci")
    invoke("always-second", "2.0.0", install="ci")
    config.write_text(base_config + "npm:\n  install: never\n")
    lock("a", "npm-shrinkwrap.json")
    invoke("never-keeps-installation", "2.0.0")
    invoke("setup-overrides-never", "1.0.0", command="setup", install="ci", record=True)
    config.write_text(base_config)
    invoke("build-reuses-setup-install", "1.0.0", reused=True)

    root = output / "unlocked-site"
    fixture(root)
    manifest = json.loads((root / "package.json").read_text())
    manifest["dependencies"]["fixture-compiler"] = "file:packages/compiler-a.tgz"
    write_json(root / "package.json", manifest)
    with (root / ".npmrc").open("a") as stream:
        stream.write("package-lock=false\n")
    invoke("no-lock-first", "1.0.0", install="install", record=False)
    invoke("no-lock-second", "1.0.0", install="install", record=False)
    print(f"通過 {len(steps)} 個真 npm 情境；證據：{output / 'receipt.json'}")


if __name__ == "__main__":
    main()
