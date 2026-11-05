#!/usr/bin/env python3
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0
"""One-off, root-only mnc-station to mps migration during an isolated maintenance window."""
import argparse
import grp
import fcntl
import json
import ipaddress
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

LOCK_FILE = Path("/run/mps-migration.lock")
OLD = "mnc-station"
OLD_STATE = Path("/var/lib/mnc-station")
NEW_STATE = Path("/var/lib/mps")
BACKUP_ROOT = Path("/var/backups")
OLD_CONFIG = Path("/etc/default/mnc-station")
NEW_CONFIG = Path("/etc/default/mps")
OLD_UNITS = ("mnc-station.service", "mnc-station-hw-server.service")
NEW_UNITS = ("mps.service", "mps-hw-server.service")
KEYS = {
    "MNC_XSDB": "MPS_XSDB", "MNC_HW_SERVER": "MPS_HW_SERVER",
    "MNC_HW_SERVER_LISTEN": "MPS_HW_SERVER_LISTEN",
    **{"MNC_STATION_" + suffix: "MPS_" + suffix for suffix in (
        "HTTP_LISTEN", "TFTP_LISTEN", "DATA_DIR", "TOKEN_FILE", "TOKEN",
        "SERIAL_BAUD", "MAX_CONSOLE_LOG_BYTES")},
}


def run(*args, check=True):
    return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def field(package, name):
    return run("dpkg-deb", "-f", str(package), name).stdout.strip()


def snapshot(path):
    if path.is_symlink():
        raise RuntimeError(f"state must not be a symlink: {path}")
    if not path.exists():
        return
    for root, dirs, files in os.walk(path, followlinks=False):
        for name in dirs + files:
            entry = Path(root) / name
            mode = entry.lstat().st_mode
            if not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
                raise RuntimeError(f"unsupported state entry: {entry}")
    if path.is_symlink():
        raise RuntimeError(f"state must not be a symlink: {path}")


def check_jobs():
    for path in (OLD_STATE / "jobs").glob("*/job.json"):
        state = json.loads(path.read_text())["state"]
        if state in ("queued", "running"):
            raise RuntimeError("queued/running jobs exist; finish or explicitly cancel them before migration")


def translated_config():
    lines = ["# Migrated from mnc-station; maintained by the operator.\n"]
    token_source = None
    settings = {}
    if not OLD_CONFIG.exists():
        return "".join(lines), token_source, settings
    for raw in OLD_CONFIG.read_text().splitlines():
        if not raw.strip() or raw.lstrip().startswith(("#", ";")):
            continue
        assignment = re.fullmatch(r"\s*([A-Z_][A-Z0-9_]*)=(.*?)\s*", raw)
        if assignment is None:
            raise RuntimeError("legacy config must contain simple KEY=value assignments only")
        key, value = assignment.groups()
        value = value.strip()
        # Support a conservative subset of systemd EnvironmentFile syntax.
        # Interior # is literal, not a shell comment. Reject escapes/mixed
        # quoting rather than changing credentials while translating them.
        if value.startswith(("'", '"')):
            quote = value[0]
            if len(value) < 2 or value[-1] != quote or quote in value[1:-1] or "\\" in value:
                raise RuntimeError("unsupported legacy config quoting; translate it manually")
            value = value[1:-1]
        elif any(c.isspace() or c in "'\"\\" for c in value):
            raise RuntimeError("unsupported legacy config quoting; translate it manually")
        if key not in KEYS:
            raise RuntimeError(f"unsupported legacy config key: {key}; translate it manually before migrating")
        if key == "MNC_STATION_TFTP_LISTEN" and value != ":69":
            raise RuntimeError("legacy unit overrides TFTP to :69; remove the ineffective custom TFTP setting before migration")
        settings[KEYS[key]] = value
        if key == "MNC_STATION_DATA_DIR" and value != str(OLD_STATE):
            raise RuntimeError("custom legacy state directories require an explicit manual migration")
        if key == "MNC_STATION_TOKEN_FILE":
            token_source = Path(value)
            if not token_source.is_absolute() or not token_source.is_file():
                raise RuntimeError("configured legacy token file must be an existing absolute file")
            value = str(NEW_STATE / "api-token")
        elif key == "MNC_STATION_DATA_DIR":
            value = str(NEW_STATE)
        # systemd EnvironmentFile accepts double quoted strings, not shell expansions.
        if any(c in value for c in ("\n", "\r", "\x00")):
            raise RuntimeError("multiline config values are not supported")
        value = value.replace("\\", "\\\\").replace('"', '\\"')
        lines.append(f'{KEYS[key]}="{value}"\n')
    return "".join(lines), token_source, settings


def unit_state(unit):
    return {
        "active": run("systemctl", "is-active", "--quiet", unit, check=False).returncode == 0,
        "enabled": run("systemctl", "is-enabled", "--quiet", unit, check=False).returncode == 0,
    }


def write_checkpoint(directory, text):
    temporary = directory / "checkpoint.next"
    with temporary.open("w") as file:
        file.write(text)
        file.flush()
        os.fsync(file.fileno())
    temporary.replace(directory / "checkpoint.json")
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def sync_backup(directory):
    for root, _, files in os.walk(directory):
        for name in files:
            with (Path(root) / name).open("rb") as file:
                os.fsync(file.fileno())
        fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def property_value(unit, name):
    return run("systemctl", "show", unit, "--property=" + name, "--value").stdout.strip()


def group_pids(unit):
    group = property_value(unit, "ControlGroup")
    if not group:
        return set()
    root = Path("/sys/fs/cgroup") / group.lstrip("/")
    pids = set()
    if root.exists():
        for file in root.rglob("cgroup.procs"):
            pids.update(int(pid) for pid in file.read_text().split())
    return pids


def reject_writers(allowed):
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit() or int(entry.name) in allowed:
            continue
        try:
            executable = (entry / "exe").resolve(strict=True).name.removesuffix(" (deleted)")
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
        if executable in ("mnc-station", "mpsd", "hw_server", "hw_server.exe"):
            raise RuntimeError(f"unmanaged provisioning/hardware process exists (PID {entry.name}); stop it explicitly")


def preflight_services(old_deb):
    # The new package is absent, so any existing definition is an unknown
    # override. Do not install into a service configuration we did not review.
    for unit in NEW_UNITS:
        if property_value(unit, "DropInPaths"):
            raise RuntimeError(f"new unit drop-ins require a manual migration: {unit}")
        if property_value(unit, "FragmentPath"):
            raise RuntimeError(f"preexisting new service fragment requires a manual migration: {unit}")
    allowed = set()
    with tempfile.TemporaryDirectory(prefix="mps-unit-preflight-") as directory:
        run("dpkg-deb", "--extract", str(old_deb), directory)
        for unit in OLD_UNITS:
            if property_value(unit, "DropInPaths"):
                raise RuntimeError(f"custom unit drop-ins require a manual migration: {unit}")
            fragment = Path(property_value(unit, "FragmentPath"))
            if fragment not in (Path("/lib/systemd/system") / unit, Path("/usr/lib/systemd/system") / unit):
                raise RuntimeError(f"custom service fragment requires a manual migration: {unit}")
            packaged = Path(directory) / "lib/systemd/system" / unit
            if not packaged.exists():
                packaged = Path(directory) / "usr/lib/systemd/system" / unit
            supported = Path(__file__).parent / "legacy-units" / unit
            if not packaged.is_file() or packaged.read_bytes() != supported.read_bytes():
                raise RuntimeError(f"unsupported legacy unit configuration requires a manual migration: {unit}")
            if fragment.read_bytes() != packaged.read_bytes():
                raise RuntimeError(f"modified service fragment requires a manual migration: {unit}")
            allowed.update(group_pids(unit))
    for unit in OLD_UNITS + NEW_UNITS:
        if run("systemctl", "is-enabled", unit, check=False).stdout.strip().startswith("masked"):
            raise RuntimeError(f"preexisting masked unit requires operator resolution: {unit}")
    reject_writers(allowed)


def verify_stopped(units):
    for unit in units:
        state = property_value(unit, "ActiveState")
        pid = property_value(unit, "MainPID")
        if state not in ("inactive", "failed", "") or pid not in ("0", "") or group_pids(unit):
            raise RuntimeError(f"service or child process did not stop: {unit}")


def job_records():
    return {path.parent.name: json.loads(path.read_text()) for path in (OLD_STATE / "jobs").glob("*/job.json")}


def check_retention_ready():
    # The new daemon enforces retention during startup. Refuse before mutation
    # rather than treating its intentional cleanup as a failed preservation.
    jobs = job_records()
    completed = sum(job.get('state') in ('succeeded', 'failed', 'canceled') for job in jobs.values())
    total = sum(path.stat().st_size for path in OLD_STATE.rglob('*') if path.is_file())
    if completed > 30 or total > 10 * 1024**3 - 4 * 1024**2:
        raise RuntimeError('legacy state exceeds retention startup limits (30 completed histories, 10 GiB minus 4 MiB maintenance reserve); run cleanup.py preview/apply first')


def check_space(old_deb, new_deb):
    total = sum(path.stat().st_size for path in OLD_STATE.rglob("*") if path.is_file()) if OLD_STATE.exists() else 0
    # Backup plus migrated copy, exact package media, and working headroom.
    required = 2 * total + old_deb.stat().st_size + new_deb.stat().st_size + 100 * 1024 * 1024
    for path in (BACKUP_ROOT if BACKUP_ROOT.exists() else BACKUP_ROOT.parent, NEW_STATE.parent):
        if shutil.disk_usage(path).free < required:
            raise RuntimeError("insufficient free space for state copy and rollback backup")


def verify_ready(settings, old_jobs, token_data):
    if token_data is not None and (NEW_STATE / "api-token").read_bytes() != token_data:
        raise RuntimeError("migrated token bytes differ")
    original_token = OLD_STATE / "api-token"
    if original_token.exists() and token_data is None and (NEW_STATE / "api-token").read_bytes() != original_token.read_bytes():
        raise RuntimeError("migrated managed token differs")
    token = settings.get("MPS_TOKEN", "").strip()
    if not token and (NEW_STATE / "api-token").exists():
        token = (NEW_STATE / "api-token").read_text().strip()
    listen = settings.get("MPS_HTTP_LISTEN", "0.0.0.0:8042")
    host, port = listen.rsplit(":", 1)
    host = host.strip("[]")
    try:
        loopback = ipaddress.ip_address(host).is_loopback
    except ValueError:
        loopback = host.lower() == "localhost"
    if loopback and not settings.get("MPS_TOKEN") and not settings.get("MPS_TOKEN_FILE"):
        token = ""
    if host in ("", "0.0.0.0", "::"):
        host = "127.0.0.1"
    address = f"[{host}]" if ":" in host else host
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    def get(path, bearer=token):
        headers = {"Host": "migration.invalid"}
        if bearer:
            headers["Authorization"] = "Bearer " + bearer
        request = urllib.request.Request(f"http://{address}:{port}/api/v1/" + path, headers=headers)
        with opener.open(request, timeout=2) as response:
            return json.load(response)
    deadline = time.monotonic() + 30
    while True:
        try:
            if get("health").get("status") == "ok":
                break
        except (OSError, ValueError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError("new API did not become ready")
        time.sleep(0.25)
    get("capabilities")
    if token:
        try:
            get("capabilities", "invalid-migration-token")
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise RuntimeError("unexpected API credential rejection") from None
        else:
            raise RuntimeError("new API did not enforce bearer authentication")
    jobs = {job["id"]: job for job in get("jobs")["jobs"]}
    if set(jobs) != set(old_jobs) or any(jobs[key]["state"] != value["state"] for key, value in old_jobs.items()):
        raise RuntimeError("migrated job IDs or terminal states differ")
    expected = {path.name for path in (OLD_STATE / "store/artifacts").iterdir()} if (OLD_STATE / "store/artifacts").exists() else set()
    actual = {artifact["id"] for artifact in get("artifacts")["artifacts"]}
    if actual != expected:
        raise RuntimeError("migrated artifact IDs differ")


def main():
    if os.geteuid() != 0:
        raise RuntimeError("run as root")
    # Serialize the entire transaction, including checkpoint preflight and
    # rollback. The root-owned /run parent and O_NOFOLLOW protect this lock.
    descriptor = os.open(LOCK_FILE, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "r+") as lock:
        os.fchmod(lock.fileno(), 0o600)
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("another migration is already running") from None
        migrate()


def migrate():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--new-deb", required=True, type=Path)
    parser.add_argument("--old-deb", type=Path, help="exact installed legacy package; otherwise search /var/cache/apt/archives")
    parser.add_argument("--maintenance-window", action="store_true", help="confirm all operators/automation are isolated and jobs are quiescent")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("run as root")
    if not args.maintenance_window:
        parser.error("--maintenance-window is required; isolate all API/UI/automation clients first")
    for command in ("dpkg-query", "dpkg-deb", "dpkg", "systemctl"):
        if not shutil.which(command):
            parser.error(f"required command missing: {command}")
    for checkpoint in BACKUP_ROOT.glob("mps-migration-*/checkpoint.json"):
        if json.loads(checkpoint.read_text()).get("phase") not in ("complete", "rolled-back", "preflight"):
            raise RuntimeError(f"unfinished migration checkpoint requires manual recovery: {checkpoint}")
    for checkpoint in BACKUP_ROOT.glob("mps-cleanup-*/checkpoint.json"):
        if json.loads(checkpoint.read_text()).get("phase") not in ("complete", "preview"):
            raise RuntimeError(f"unfinished cleanup requires cleanup.py resume: {checkpoint}")
    installed = run("dpkg-query", "-W", "-f=${Status}\n${Version}\n${Architecture}", OLD).stdout.splitlines()
    if len(installed) != 3 or installed[0] != "install ok installed":
        raise RuntimeError("legacy mnc-station package must be fully installed")
    if run("dpkg-query", "-W", "-f=${Status}", "mps", check=False).stdout.strip() == "install ok installed":
        raise RuntimeError("mps is already installed; use the normal package upgrade procedure")
    new_deb = args.new_deb.resolve(strict=True)
    if field(new_deb, "Package") != "mps" or field(new_deb, "Architecture") != installed[2]:
        raise RuntimeError("new package name or architecture does not match this migration")
    candidates = [args.old_deb.resolve(strict=True)] if args.old_deb else sorted(Path("/var/cache/apt/archives").glob("mnc-station_*.deb"))
    old_deb = next((p for p in candidates if field(p, "Package") == OLD and field(p, "Version") == installed[1] and field(p, "Architecture") == installed[2]), None)
    if old_deb is None:
        raise RuntimeError("matching legacy .deb is required for rollback; supply --old-deb or retain it in the apt cache")
    if NEW_STATE.is_symlink() or (NEW_STATE.exists() and any(NEW_STATE.iterdir())):
        raise RuntimeError("new state directory must be absent or empty")
    if NEW_CONFIG.exists() or NEW_CONFIG.is_symlink():
        raise RuntimeError("new config already exists; preserve and resolve it manually before migration")
    snapshot(OLD_STATE)
    check_jobs()
    check_retention_ready()
    config, token_source, settings = translated_config()
    preflight_services(old_deb)
    before_jobs = job_records()
    check_space(old_deb, new_deb)
    token_data = token_source.read_bytes() if token_source else None
    if token_data is not None and (not token_data.strip() or len(token_data) > 4096):
        raise RuntimeError("legacy token file is empty or too large")
    backup_root = BACKUP_ROOT
    backup_root.mkdir(mode=0o755, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="mps-migration-", dir=backup_root))
    os.chmod(backup, 0o700)
    states = {unit: unit_state(unit) for unit in OLD_UNITS}
    write_checkpoint(backup, json.dumps({"phase": "preflight", "old_units": states, "old_version": installed[1]}, indent=2))
    shutil.copy2(old_deb, backup / "old.deb")
    shutil.copy2(new_deb, backup / "new.deb")
    if OLD_CONFIG.exists():
        shutil.copy2(OLD_CONFIG, backup / "old-config")
    mutated = False
    removed_old = False
    success = False
    masked = []
    try:
        mutated = True
        write_checkpoint(backup, json.dumps({"phase": "stopping", "old_units": states, "old_version": installed[1]}, indent=2))
        for unit in OLD_UNITS:
            run("systemctl", "stop", unit)
        # Clients must remain isolated throughout the window. Recheck persisted
        # work after stop; a queued/running record requires operator attention.
        verify_stopped(OLD_UNITS)
        reject_writers(set())
        check_jobs()
        if job_records() != before_jobs:
            raise RuntimeError("legacy job records changed during shutdown; investigate before retrying")
        snapshot(OLD_STATE)
        if OLD_STATE.exists():
            shutil.copytree(OLD_STATE, backup / "old-state")
        sync_backup(backup)
        write_checkpoint(backup, json.dumps({"phase": "backed-up", "old_units": states, "old_version": installed[1]}, indent=2))
        for unit in OLD_UNITS + NEW_UNITS:
            run("systemctl", "mask", "--runtime", unit)
            masked.append(unit)
        NEW_STATE.mkdir(mode=0o700, exist_ok=True)
        (NEW_STATE / ".migration-in-progress").touch(mode=0o600)
        removed_old = True
        run("dpkg", "--remove", OLD)
        run("dpkg", "--install", str(backup / "new.deb"))
        if (backup / "old-state").exists():
            shutil.copytree(backup / "old-state", NEW_STATE, dirs_exist_ok=True)
        if token_data is not None:
            (NEW_STATE / "api-token").write_bytes(token_data)
        NEW_CONFIG.write_text(config)
        uid, gid = pwd.getpwnam("mps").pw_uid, grp.getgrnam("mps").gr_gid
        os.chmod(NEW_CONFIG, 0o640)
        os.chown(NEW_CONFIG, 0, gid)
        for root, dirs, files in os.walk(NEW_STATE):
            os.chown(root, uid, gid)
            os.chmod(root, 0o700)
            for name in files:
                path = Path(root) / name
                os.chown(path, uid, gid)
                relative = path.relative_to(NEW_STATE).parts
                # Payload mode is part of the artifact manifest contract. The
                # private ancestor directories protect readable/executable files.
                payload = len(relative) >= 5 and relative[:2] == ("store", "artifacts") and relative[3] == "payload"
                if not payload:
                    os.chmod(path, 0o600)
        (NEW_STATE / ".migration-in-progress").unlink()
        run("systemctl", "unmask", "--runtime", *masked)
        masked.clear()
        run("systemctl", "daemon-reload")
        run("systemctl", "enable", "mps.service")
        run("systemctl", "start", "mps.service")
        run("systemctl", "is-active", "--quiet", "mps.service")
        verify_ready(settings, before_jobs, token_data)
        success = True
        write_checkpoint(backup, json.dumps({"phase": "complete", "old_units": states, "old_version": installed[1]}, indent=2))
        print(f"Migration complete. Private rollback backup: {backup}")
    except BaseException:
        if mutated:
            failures = []
            def rollback(*command):
                result = run(*command, check=False)
                if result.returncode:
                    failures.append(" ".join(command))
            for unit in OLD_UNITS + NEW_UNITS:
                if unit not in masked and run("systemctl", "mask", "--runtime", unit, check=False).returncode == 0:
                    masked.append(unit)
            for unit in NEW_UNITS:
                run("systemctl", "stop", unit, check=False)
            try:
                verify_stopped(NEW_UNITS)
                reject_writers(set())
            except (RuntimeError, subprocess.CalledProcessError):
                write_checkpoint(backup, json.dumps({"phase": "rollback-incomplete", "reason": "new service/process did not stop; old service was not restarted", "old_units": states}, indent=2))
                print(f"Rollback blocked by a live writer; keep clients isolated. Private backup: {backup}", file=sys.stderr)
                # Leave masks in place; do not permit concurrent old/new writers.
                masked.clear()
                raise
            # Keep failed new state available for investigation; the original
            # legacy state was copied, never moved or modified.
            if removed_old:
                rollback("dpkg", "--remove", "mps")
            if NEW_STATE.exists():
                shutil.move(str(NEW_STATE), backup / "failed-new-state")
            if NEW_CONFIG.exists():
                shutil.move(str(NEW_CONFIG), backup / "failed-new-config")
            if removed_old:
                rollback("dpkg", "--install", str(backup / "old.deb"))
            if (backup / "old-config").exists():
                shutil.copy2(backup / "old-config", OLD_CONFIG)
            for unit in masked:
                rollback("systemctl", "unmask", "--runtime", unit)
            masked.clear()
            rollback("systemctl", "daemon-reload")
            for unit, state in states.items():
                rollback("systemctl", "enable" if state["enabled"] else "disable", unit)
                rollback("systemctl", "start" if state["active"] else "stop", unit)
            write_checkpoint(backup, json.dumps({"phase": "rollback-incomplete" if failures else "rolled-back", "old_units": states, "failed_commands": failures}, indent=2))
            print(f"Migration failed; rollback {'needs attention' if failures else 'completed'}. Private backup: {backup}", file=sys.stderr)
        raise
    finally:
        if not success:
            for unit in masked:
                run("systemctl", "unmask", "--runtime", unit, check=False)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.CalledProcessError) as error:
        # Never print subprocess output or config values: either may contain secrets.
        print(f"Migration stopped: {error}", file=sys.stderr)
        sys.exit(1)
