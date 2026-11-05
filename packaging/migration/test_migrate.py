#!/usr/bin/env python3
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0
"""Isolated migration failure tests. Never invokes host package/service tools."""
import importlib.util
import fcntl
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from contextlib import ExitStack, redirect_stderr, redirect_stdout
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("migration", Path(__file__).with_name("migrate.py"))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)
REAL_PREFLIGHT = migration.preflight_services


class MigrationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        for key, suffix in (("LOCK_FILE", "migration.lock"), ("OLD_STATE", "old-state"), ("NEW_STATE", "new-state"),
                            ("OLD_CONFIG", "old-config"), ("NEW_CONFIG", "new-config"),
                            ("BACKUP_ROOT", "backups")):
            self.stack.enter_context(patch.object(migration, key, self.root / suffix))
        migration.OLD_STATE.mkdir()
        (migration.OLD_STATE / "api-token").write_text("private-token")
        (migration.OLD_STATE / "jobs" / "done").mkdir(parents=True)
        (migration.OLD_STATE / "jobs" / "done" / "job.json").write_text('{"state":"succeeded"}')
        migration.OLD_CONFIG.write_text('MNC_STATION_HTTP_LISTEN=127.0.0.1:8042\n')
        self.old = self.root / "old.deb"
        self.new = self.root / "new.deb"
        self.old.touch()
        self.new.touch()
        self.commands = []
        self.failure = None
        self.package = "mnc-station"
        self.stack.enter_context(patch.object(migration, "run", self.fake_run))
        self.stack.enter_context(patch.object(migration, "preflight_services"))
        self.stack.enter_context(patch.object(migration, "reject_writers"))
        self.ready = self.stack.enter_context(patch.object(migration, "verify_ready"))
        self.stack.enter_context(patch.object(migration.os, "geteuid", return_value=0))
        self.stack.enter_context(patch.object(migration.os, "chown"))
        self.stack.enter_context(patch.object(migration.shutil, "which", return_value="mock"))
        self.stack.enter_context(patch.object(migration.pwd, "getpwnam", return_value=types.SimpleNamespace(pw_uid=os.getuid())))
        self.stack.enter_context(patch.object(migration.grp, "getgrnam", return_value=types.SimpleNamespace(gr_gid=os.getgid())))
        self.stack.enter_context(patch.object(sys, "argv", ["migrate.py", "--new-deb", str(self.new), "--old-deb", str(self.old), "--maintenance-window"]))

    def fake_run(self, *args, check=True):
        self.commands.append(args)
        result = subprocess.CompletedProcess(args, 0, "", "")
        if self.failure and args[:len(self.failure)] == self.failure:
            self.failure = None
            result.returncode = 1
        elif args[0] == "dpkg-query":
            if args[-1] == "mnc-station":
                result.stdout = "install ok installed\n1.2.3\namd64"
            else:
                result.stdout = "unknown ok not-installed"
        elif args[:2] == ("dpkg-deb", "-f"):
            old = "old.deb" in args[2]
            result.stdout = {"Package": "mnc-station" if old else "mps", "Version": "1.2.3" if old else "1.2.4", "Architecture": "amd64"}[args[-1]]
        elif args[:2] == ("dpkg", "--install"):
            self.package = "mnc-station" if "old.deb" in args[2] else "mps"
        if check and result.returncode:
            raise subprocess.CalledProcessError(result.returncode, args)
        return result

    def execute(self):
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            migration.main()

    def test_success_preserves_data_and_token_and_config(self):
        self.execute()
        self.assertEqual((migration.NEW_STATE / "api-token").read_text(), "private-token")
        self.assertEqual((migration.OLD_STATE / "api-token").read_text(), "private-token")
        self.assertIn('MPS_HTTP_LISTEN="127.0.0.1:8042"', migration.NEW_CONFIG.read_text())
        self.assertEqual((migration.NEW_STATE / "api-token").stat().st_mode & 0o777, 0o600)
        self.assertFalse((migration.NEW_STATE / ".migration-in-progress").exists())
        backup = next(migration.BACKUP_ROOT.iterdir())
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)
        self.assertEqual(json.loads((backup / "checkpoint.json").read_text())["phase"], "complete")

    def test_concurrent_migration_refused_before_preflight(self):
        with migration.LOCK_FILE.open("w") as lock:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(RuntimeError, "already running"):
                self.execute()
        self.assertFalse(self.commands)

    def test_token_hash_is_literal_in_systemd_config(self):
        token = "abcdef1234567890#suffix"
        migration.OLD_CONFIG.write_text("MNC_STATION_TOKEN=" + token + "\n")
        self.execute()
        self.assertIn('MPS_TOKEN="' + token + '"', migration.NEW_CONFIG.read_text())
        self.assertEqual(self.ready.call_args.args[0]["MPS_TOKEN"], token)

    def test_ambiguous_config_rejected_before_stop(self):
        for value in ('abc"def"', '"abc"suffix', 'abc\\def'):
            migration.OLD_CONFIG.write_text("MNC_STATION_TOKEN=" + value + "\n")
            with self.assertRaisesRegex(RuntimeError, "quoting"):
                self.execute()
        self.assertFalse(any(c[:2] == ("systemctl", "stop") for c in self.commands))

    def test_preserves_payload_modes_and_transcript(self):
        payload = migration.OLD_STATE / "store/artifacts/retained/payload"
        payload.mkdir(parents=True)
        for name, mode in (("image", 0o644), ("loader", 0o755)):
            file = payload / name
            file.write_bytes(b"retained payload")
            file.chmod(mode)
        transcript = migration.OLD_STATE / "jobs/done/serial.log"
        transcript.write_bytes(b"retained serial\x00\xff\n")
        self.execute()
        for name, mode in (("image", 0o644), ("loader", 0o755)):
            copied = migration.NEW_STATE / payload.relative_to(migration.OLD_STATE) / name
            self.assertEqual(copied.stat().st_mode & 0o777, mode)
        self.assertEqual((migration.NEW_STATE / "jobs/done/serial.log").read_bytes(), transcript.read_bytes())

    def test_failure_rolls_back_at_each_mutation_boundary(self):
        for command in (("systemctl", "stop"), ("systemctl", "mask"), ("dpkg", "--remove"),
                        ("dpkg", "--install"), ("systemctl", "enable"), ("systemctl", "start")):
            with self.subTest(command=command):
                # Each case gets a fresh isolated filesystem.
                case = MigrationTest()
                case.setUp()
                try:
                    case.failure = command
                    with self.assertRaises(subprocess.CalledProcessError):
                        case.execute()
                    self.assertEqual((migration.OLD_STATE / "api-token").read_text(), "private-token")
                    backup = next(migration.BACKUP_ROOT.iterdir())
                    self.assertEqual(json.loads((backup / "checkpoint.json").read_text())["phase"], "rolled-back")
                    self.assertEqual(case.package, "mnc-station")
                finally:
                    case.doCleanups()

    def test_active_jobs_rejected_before_services_are_touched(self):
        (migration.OLD_STATE / "jobs" / "done" / "job.json").write_text('{"state":"running"}')
        with self.assertRaisesRegex(RuntimeError, "queued/running"):
            self.execute()
        self.assertFalse(any(c[0] == "systemctl" for c in self.commands))

    def test_wrong_rollback_version_rejected_before_services_are_touched(self):
        original = migration.field
        with patch.object(migration, "field", side_effect=lambda p, name: "wrong" if p == self.old and name == "Version" else original(p, name)):
            with self.assertRaisesRegex(RuntimeError, "matching legacy"):
                self.execute()
        self.assertFalse(any(c[0] == "systemctl" for c in self.commands))

    def test_state_link_rejected_before_services_are_touched(self):
        (migration.OLD_STATE / "link").symlink_to(self.old)
        with self.assertRaisesRegex(RuntimeError, "unsupported state"):
            self.execute()
        self.assertFalse(any(c[0] == "systemctl" for c in self.commands))

    def test_unfinished_checkpoint_blocks_before_package_queries(self):
        previous = migration.BACKUP_ROOT / "mps-migration-interrupted"
        previous.mkdir(parents=True)
        (previous / "checkpoint.json").write_text('{"phase":"backed-up"}')
        with self.assertRaisesRegex(RuntimeError, "unfinished migration"):
            self.execute()
        self.assertEqual(self.commands, [])

    def test_readiness_failure_rolls_back(self):
        self.ready.side_effect = RuntimeError("retained IDs differ")
        with self.assertRaisesRegex(RuntimeError, "retained IDs"):
            self.execute()
        self.assertEqual(self.package, "mnc-station")
        self.assertTrue(any(c[:2] == ("systemctl", "start") and c[-1] == "mnc-station.service" for c in self.commands))

    def test_live_new_writer_prevents_old_restart(self):
        self.failure = ("dpkg", "--install")
        def stopped(units):
            if units == migration.NEW_UNITS:
                raise RuntimeError("live new writer")
        with patch.object(migration, "verify_stopped", side_effect=stopped):
            with self.assertRaisesRegex(RuntimeError, "live new writer"):
                self.execute()
        self.assertFalse(any(c[:2] == ("systemctl", "start") for c in self.commands))
        self.assertFalse(any(c[:2] == ("dpkg", "--install") and c[-1].endswith("old.deb") for c in self.commands))
        backup = next(migration.BACKUP_ROOT.iterdir())
        self.assertEqual(json.loads((backup / "checkpoint.json").read_text())["phase"], "rollback-incomplete")

    def test_job_changed_during_stop_does_not_remove_package(self):
        original = migration.run
        def stop_and_change(*args, **kwargs):
            result = original(*args, **kwargs)
            if args[:2] == ("systemctl", "stop"):
                (migration.OLD_STATE / "jobs" / "done" / "job.json").write_text('{"state":"failed"}')
            return result
        with patch.object(migration, "run", side_effect=stop_and_change):
            with self.assertRaisesRegex(RuntimeError, "changed during shutdown"):
                self.execute()
        self.assertFalse(any(c[0] == "dpkg" for c in self.commands))

    def test_insufficient_space_does_not_mutate_services(self):
        with patch.object(migration.shutil, "disk_usage", return_value=types.SimpleNamespace(free=0)):
            with self.assertRaisesRegex(RuntimeError, "insufficient free"):
                self.execute()
        self.assertFalse(any(c[:2] == ("systemctl", "stop") for c in self.commands))

    def test_ineffective_legacy_tftp_override_requires_resolution(self):
        migration.OLD_CONFIG.write_text('MNC_STATION_TFTP_LISTEN=:6969\n')
        with self.assertRaisesRegex(RuntimeError, "overrides TFTP"):
            self.execute()
        self.assertFalse(any(c[:2] == ("systemctl", "stop") for c in self.commands))

    def test_broken_root_state_link_rejected(self):
        import shutil
        shutil.rmtree(migration.OLD_STATE)
        migration.OLD_STATE.symlink_to(self.root / "missing")
        with self.assertRaisesRegex(RuntimeError, "symlink"):
            self.execute()

    def test_broken_new_state_link_rejected(self):
        migration.NEW_STATE.symlink_to(self.root / "missing")
        with self.assertRaisesRegex(RuntimeError, "absent or empty"):
            self.execute()

    def test_real_preflight_rejects_dropin(self):
        with patch.object(migration, "property_value", return_value="/etc/systemd/system/mnc-station.service.d/override.conf"):
            with self.assertRaisesRegex(RuntimeError, "drop-ins"):
                REAL_PREFLIGHT(self.old)

    def test_real_preflight_rejects_new_unit_override(self):
        for prop in ("DropInPaths", "FragmentPath"):
            with self.subTest(property=prop):
                def properties(unit, name):
                    return "/etc/systemd/system/mps.service" if unit == migration.NEW_UNITS[0] and name == prop else ""
                with patch.object(migration, "property_value", side_effect=properties):
                    with self.assertRaisesRegex(RuntimeError, "new .*manual migration"):
                        REAL_PREFLIGHT(self.old)

    def test_real_preflight_rejects_same_custom_unit_in_package_and_install(self):
        expected = Path(migration.__file__).parent / "legacy-units"
        def extract(*args, **kwargs):
            if args[:2] == ("dpkg-deb", "--extract"):
                destination = Path(args[-1]) / "lib/systemd/system"
                destination.mkdir(parents=True)
                for unit in migration.OLD_UNITS:
                    data = (expected / unit).read_text().replace("--data-dir=/var/lib/mnc-station", "--data-dir=/custom-state")
                    (destination / unit).write_text(data)
                return subprocess.CompletedProcess(args, 0, "", "")
            return self.fake_run(*args, **kwargs)
        def properties(unit, name):
            return "" if unit in migration.NEW_UNITS or name == "DropInPaths" else "/lib/systemd/system/" + unit
        with patch.object(migration, "run", side_effect=extract), patch.object(migration, "property_value", side_effect=properties):
            with self.assertRaisesRegex(RuntimeError, "unsupported legacy unit"):
                REAL_PREFLIGHT(self.old)


if __name__ == "__main__":
    unittest.main()
