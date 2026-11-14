"""Install two pushed SDK revisions as isolated consumers and run the live OIDC probe."""

import hashlib
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


def prepare(revision, folder):
    source = folder / "source"
    source.mkdir(parents=True)
    exported = subprocess.check_output(["git", "archive", revision], cwd=ROOT, timeout=30)
    run(["tar", "-xf", "-"], source, input=exported)
    run(["pnpm", "install", "--frozen-lockfile"], source, env=dict(os.environ, CI="true"))
    run(["pnpm", "run", "build"], source)
    archive_dir = folder / "archive"
    archive_dir.mkdir()
    run(["pnpm", "--filter", "@heimdall/sdk", "pack", "--pack-destination", str(archive_dir)], source)
    archive, = archive_dir.glob("*.tgz")
    ts = folder / "ts"
    ts.mkdir()
    shutil.copyfile(ROOT / "tests/integration/mixed-sdk-probe.mjs", ts / "probe.mjs")
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
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    print(f"Prepared {revision} with TS archive sha256 {digest} and Go module {version}", flush=True)
    return digest


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] == sys.argv[2] or any(
        not re.fullmatch("[0-9a-f]{40}", revision) for revision in sys.argv[1:]
    ):
        raise SystemExit("Pass two distinct pushed SDK commit hashes")
    with tempfile.TemporaryDirectory(prefix="heimdall-mixed-sdk-") as directory:
        root = Path(directory)
        previous = prepare(sys.argv[1], root / "previous")
        candidate = prepare(sys.argv[2], root / "candidate")
        if previous == candidate:
            raise RuntimeError("The supplied revisions produced identical TypeScript packages")
        env = dict(os.environ, HEIMDALL_PROBE_MIXED_SDK=str(root), HEIMDALL_PROBE_SDK_RUNTIME="1")
        env.pop("HEIMDALL_TEST_RUNTIME_DATABASE_URL", None)
        run(["node", "tests/integration/oidc.mjs"], env=env)
