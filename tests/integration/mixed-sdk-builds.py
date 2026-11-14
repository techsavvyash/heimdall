"""Install two pushed SDK revisions as isolated consumers and run the live OIDC probe."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
MODULE = "github.com/techsavvyash/heimdall/sdk/go"


def run(args, cwd=ROOT, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, timeout=180, **kwargs)


def prepare(revision, folder, archive=None):
    if archive is None:
        source = folder / "source"
        source.mkdir(parents=True)
        exported = subprocess.check_output(["git", "archive", revision], cwd=ROOT, timeout=30)
        run(["tar", "-xf", "-"], source, input=exported)
        run(["pnpm", "install", "--frozen-lockfile"], source, env=dict(os.environ, CI="true"))
        run(["pnpm", "run", "build"], source)
        archive_dir = folder / "archive"
        archive_dir.mkdir()
        run(["pnpm", "pack", "--pack-destination", str(archive_dir)], source / "sdk/typescript")
        archive, = archive_dir.glob("*.tgz")
    ts = folder / "ts"
    ts.mkdir(parents=True)
    metadata = json.loads(subprocess.check_output(["tar", "-xOzf", str(archive), "package/package.json"]))
    probe = (ROOT / "tests/integration/mixed-sdk-probe.mjs").read_text()
    (ts / "probe.mjs").write_text(probe.replace("@techsavvyash/heimdall-sdk", metadata["name"]))
    (ts / "package.json").write_text('{"type":"module"}\n')
    (ts / "pnpm-workspace.yaml").write_text("minimumReleaseAge: 43200\nminimumReleaseAgeStrict: true\n")
    run(["pnpm", "add", "--ignore-scripts", str(archive)], ts)
    go = folder / "go"
    go.mkdir()
    env = dict(os.environ, GOWORK="off")
    shutil.copyfile(ROOT / "sdk/go/client_live_test.go", go / "runtime_test.go")
    (go / "workspace_test.go").write_text(f"""package heimdall_test

import (
    "context"
    heimdall "{MODULE}"
)

type workspaceScan func(context.Context, heimdall.ScanOptions) (heimdall.CandidatePage, error)

func (scan workspaceScan) Scan(ctx context.Context, options heimdall.ScanOptions) (heimdall.CandidatePage, error) {{
    return scan(ctx, options)
}}
""")
    run(["go", "mod", "init", "example.test/mixed-sdk-consumer"], go, env=env)
    run(["go", "get", MODULE + "@" + revision], go, env=env)
    run(["go", "mod", "tidy"], go, env=env)
    run(["go", "test", "-race", "-c", "-o", str(folder / "go-probe")], go, env=env)
    version = run(["go", "list", "-m", "-f", "{{.Version}}", MODULE],
                  go, env=env, capture_output=True, text=True).stdout.strip()
    if revision.startswith("v") and version != revision:
        raise RuntimeError("Go module did not resolve to the requested candidate")
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    print(f"Prepared {revision} with TS archive sha256 {digest} and Go module {version}", flush=True)
    return digest


if __name__ == "__main__":
    if len(sys.argv) not in (3, 4) or sys.argv[1] == sys.argv[2] or not re.fullmatch("[0-9a-f]{40}", sys.argv[1]):
        raise SystemExit("Pass two distinct pushed commits, or a previous commit, candidate tag and matching archive")
    archive = None
    if len(sys.argv) == 4:
        if not re.fullmatch(r"v\d+\.\d+\.\d+-rc\.[1-9]\d*", sys.argv[2]):
            raise SystemExit("The supplied archive requires a candidate tag")
        archive = Path(sys.argv[3]).resolve(strict=True)
        if archive.name not in {f"{name}-{sys.argv[2][1:]}.tgz" for name in ("heimdall-sdk", "techsavvyash-heimdall-sdk")}:
            raise SystemExit("The archive name must match the candidate version")
    elif not re.fullmatch("[0-9a-f]{40}", sys.argv[2]):
        raise SystemExit("Pass a pushed Git commit to build the candidate archive")
    with tempfile.TemporaryDirectory(prefix="heimdall-mixed-sdk-") as directory:
        root = Path(directory)
        previous = prepare(sys.argv[1], root / "previous")
        candidate = prepare(sys.argv[2], root / "candidate", archive)
        if previous == candidate:
            raise RuntimeError("The supplied revisions produced identical TypeScript packages")
        env = dict(os.environ, HEIMDALL_PROBE_MIXED_SDK=str(root), HEIMDALL_PROBE_SDK_RUNTIME="1")
        env.pop("HEIMDALL_TEST_RUNTIME_DATABASE_URL", None)
        run(["node", "tests/integration/oidc.mjs"], env=env)
