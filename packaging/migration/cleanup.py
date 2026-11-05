#!/usr/bin/env python3
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0
"""Explicit, irreversible legacy cleanup. Preview first; apply only in maintenance."""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import sys
import tarfile

import migrate as migration

TERMINAL = {'succeeded', 'failed', 'canceled'}
ID = re.compile(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\Z')


def load(path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise RuntimeError('duplicate JSON key')
            result[key] = value
        return result
    return json.loads(path.read_text(), object_pairs_hook=unique)


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def safe(relative):
    path = PurePosixPath(relative)
    if relative in ('', '.', '..') or path.is_absolute() or str(path) != relative or any(p in ('.', '..') for p in path.parts) or '\\' in relative:
        raise RuntimeError('unsafe inventory path')
    return path


def inventory():
    root = migration.OLD_STATE
    migration.snapshot(root)
    if not root.is_dir():
        raise RuntimeError('legacy state directory missing')
    result = {}
    for path in sorted(root.rglob('*')):
        info = path.lstat()
        if stat.S_ISREG(info.st_mode):
            if info.st_nlink != 1:
                raise RuntimeError('hard-linked state file')
            relative = path.relative_to(root).as_posix()
            safe(relative)
            result[relative] = {'size': info.st_size, 'sha256': digest(path),
                                'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}
    for temporary in ('store/tmp', 'jobs/.staging'):
        path = root / temporary
        if path.exists() and any(path.iterdir()):
            raise RuntimeError('interrupted import/job staging requires manual recovery')
    return result


def credentials():
    _, token_source, _ = migration.translated_config()
    result = {}
    for path in (migration.OLD_CONFIG, token_source):
        if path is not None and path.exists():
            if path.is_symlink():
                raise RuntimeError('config/token symlink requires manual migration')
            result[str(path)] = inventory_entry(path)
    return result


def timestamp(value):
    if not isinstance(value, str):
        raise RuntimeError('job timestamp missing')
    try:
        result = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
        if result.tzinfo is None:
            raise ValueError()
        return result
    except ValueError:
        raise RuntimeError('invalid job timestamp') from None


def validate_metadata(record):
    manifest = record.get('manifest')
    if not isinstance(manifest, dict) or manifest.get('schema') != 'mnc-station-artifact' or manifest.get('formatVersion') != 2:
        raise RuntimeError('invalid artifact manifest schema/version')
    metadata = manifest.get('artifact')
    executor = manifest.get('executor')
    if not isinstance(metadata, dict) or any(not isinstance(metadata.get(key), str) or not metadata[key] for key in ('name', 'vendor', 'operation', 'product', 'machine', 'version', 'buildId', 'createdUtc')):
        raise RuntimeError('invalid artifact metadata')
    timestamp(metadata['createdUtc'])
    if not isinstance(executor, dict) or not isinstance(executor.get('type'), str) or not executor['type']:
        raise RuntimeError('invalid artifact executor')
    safe(executor.get('entrypoint', ''))
    files = manifest.get('files')
    if not isinstance(files, dict) or not files or executor['entrypoint'] not in files:
        raise RuntimeError('invalid artifact file set')
    for name, descriptor in files.items():
        safe(name)
        if name in ('manifest.json', 'manifest.sig') or not isinstance(descriptor, dict) or type(descriptor.get('size')) is not int or descriptor['size'] < 0 or not isinstance(descriptor.get('sha256'), str) or not re.fullmatch('[0-9a-f]{64}', descriptor['sha256']) or descriptor.get('mode') not in ('0644', '0755'):
            raise RuntimeError('invalid artifact file descriptor')


def service_preflight():
    allowed = set()
    for unit in migration.OLD_UNITS:
        if migration.property_value(unit, 'DropInPaths'):
            raise RuntimeError('custom legacy service drop-ins require manual cleanup')
        fragment = Path(migration.property_value(unit, 'FragmentPath'))
        if fragment not in (Path('/lib/systemd/system') / unit, Path('/usr/lib/systemd/system') / unit):
            raise RuntimeError('custom or absent legacy service requires manual cleanup')
        if fragment.read_bytes() != (Path(__file__).parent / 'legacy-units' / unit).read_bytes():
            raise RuntimeError('modified legacy service requires manual cleanup')
        allowed.update(migration.group_pids(unit))
    migration.reject_writers(allowed)


def make_plan():
    files = inventory()
    root = migration.OLD_STATE
    jobs, artifacts = {}, {}
    for directory, result, filename in (('jobs', jobs, 'job.json'), ('store/artifacts', artifacts, 'artifact.json')):
        parent = root / directory
        if not parent.is_dir():
            raise RuntimeError('legacy jobs/artifacts directory missing')
        for path in parent.iterdir():
            if not path.is_dir() or not ID.fullmatch(path.name):
                raise RuntimeError('unknown or unsafe job/artifact entry')
            record = load(path / filename)
            if directory == 'store/artifacts' and not re.fullmatch('[0-9a-f]{64}', path.name):
                raise RuntimeError('invalid artifact ID')
            if record.get('id') != path.name:
                raise RuntimeError('record ID differs from directory')
            if directory == 'store/artifacts':
                validate_metadata(record)
            result[path.name] = record
    ordered = []
    protected = []
    for key, job in jobs.items():
        if job.get('state') not in TERMINAL:
            raise RuntimeError('active/unknown job state; finish or cancel before cleanup')
        ordered.append((timestamp(job.get('createdUtc')), key))
        request = job.get('request')
        if not isinstance(request, dict) or request.get('artifactId') not in artifacts:
            raise RuntimeError('job has missing or invalid artifact reference')
        if request.get('requestId'):
            if not re.fullmatch('[0-9a-f]{32}', request['requestId']):
                raise RuntimeError('invalid requestId')
            protected.append(key)
    keep_jobs = set(key for _, key in sorted(ordered, reverse=True)[:30])
    if set(protected) - keep_jobs:
        raise RuntimeError('older requestId identity requires manual migration; cleanup refuses to prune it')
    keep_artifacts = {jobs[key]['request']['artifactId'] for key in keep_jobs}
    removed_jobs = sorted(set(jobs) - keep_jobs)
    removed_artifacts = sorted(set(artifacts) - keep_artifacts)
    prefixes = [f'jobs/{key}/' for key in removed_jobs] + [f'store/artifacts/{key}/' for key in removed_artifacts]
    remove = sorted(name for name in files if any(name.startswith(prefix) for prefix in prefixes))
    remaining = sum(value['size'] for name, value in files.items() if name not in set(remove))
    if remaining > 10 * 1024**3 - 4 * 1024**2:
        raise RuntimeError('latest 30 retained histories exceed 10 GiB budget with 4 MiB maintenance reserve; manual migration required')
    return {'version': 1, 'state': str(root), 'files': files, 'remove': remove,
            'keepJobs': sorted(keep_jobs), 'keepArtifacts': sorted(keep_artifacts),
            'removeJobs': removed_jobs, 'removeArtifacts': removed_artifacts,
            'requestProtectedJobs': sorted(protected),
            'deleteBytes': sum(files[name]['size'] for name in remove),
            'retainedBytes': remaining, 'credentials': credentials(),
            'artifactBytes': {key: sum(value['size'] for name, value in files.items() if name.startswith(f'store/artifacts/{key}/')) for key in sorted(artifacts)},
            'jobBytes': {key: sum(value['size'] for name, value in files.items() if name.startswith(f'jobs/{key}/')) for key in sorted(jobs)}}


def verify_artifact(key):
    root = migration.OLD_STATE / 'store/artifacts' / key
    record = load(root / 'artifact.json')
    if digest(root / 'artifact.tar.gz') != key:
        raise RuntimeError('retained artifact archive checksum differs')
    manifest = record['manifest']
    if load(root / 'payload/manifest.json') != manifest:
        raise RuntimeError('retained manifest differs')
    expected = {'manifest.json'} | set(manifest['files'])
    if record.get('hasSignature'):
        expected.add('manifest.sig')
    actual = {p.relative_to(root / 'payload').as_posix() for p in (root / 'payload').rglob('*') if p.is_file()}
    if actual != expected:
        raise RuntimeError('retained payload file set differs')
    for name, descriptor in manifest['files'].items():
        safe(name)
        path = root / 'payload' / name
        if path.stat().st_size != descriptor['size'] or digest(path) != descriptor['sha256'] or stat.S_IMODE(path.stat().st_mode) != int(descriptor['mode'], 8):
            raise RuntimeError('retained payload checksum/size/mode differs')
    # Never extract archives here. Verify each archived payload against the same
    # manifest; reject unsafe/duplicate/link/device/unrecorded archive entries.
    seen = set()
    with tarfile.open(root / 'artifact.tar.gz', 'r:gz') as archive:
        for member in archive:
            safe(member.name)
            if not member.isfile() or member.name in seen or member.name not in expected:
                raise RuntimeError('unsafe retained archive entry')
            seen.add(member.name)
            stream = archive.extractfile(member)
            if member.name == 'manifest.json':
                if member.size > 4 * 1024 * 1024 or json.load(stream) != manifest:
                    raise RuntimeError('archive manifest differs')
            elif member.name == 'manifest.sig':
                if member.size != (root / 'payload/manifest.sig').stat().st_size or hashlib.file_digest(stream, 'sha256').hexdigest() != digest(root / 'payload/manifest.sig'):
                    raise RuntimeError('archive signature differs')
            else:
                descriptor = manifest['files'][member.name]
                if member.size != descriptor['size'] or member.mode != int(descriptor['mode'], 8) or hashlib.file_digest(stream, 'sha256').hexdigest() != descriptor['sha256']:
                    raise RuntimeError('archive payload differs')
    if seen != expected:
        raise RuntimeError('archive missing payload')


def save(directory, record):
    migration.write_checkpoint(directory, json.dumps(record, indent=2))


def private(path):
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o700:
        raise RuntimeError('plan directory must be root-owned mode 0700')
    for name in ('checkpoint.json', 'plan.json'):
        file = path / name
        if file.is_symlink() or not file.is_file() or file.stat().st_uid != 0:
            raise RuntimeError('unsafe cleanup plan file')


def validate_remaining(plan):
    if credentials() != plan['credentials']:
        raise RuntimeError('credentials/config changed since preview')
    current = inventory()
    deleted = set(plan['remove'])
    for name, details in current.items():
        if name not in plan['files'] or details != plan['files'][name]:
            raise RuntimeError('state changed since preview; services remain stopped')
    if set(plan['files']) - set(current) - deleted:
        raise RuntimeError('retained state missing; services remain stopped')
    return current


def execute(directory, resume=False):
    private(directory)
    plan = load(directory / 'plan.json')
    if plan.get('version') != 1 or plan.get('state') != str(migration.OLD_STATE):
        raise RuntimeError('plan does not match legacy state')
    for name in plan['files']:
        safe(name)
    for key in plan['keepArtifacts'] + plan['removeArtifacts']:
        if not re.fullmatch('[0-9a-f]{64}', key):
            raise RuntimeError('invalid artifact ID')
    for key in plan['removeJobs']:
        if not ID.fullmatch(key):
            raise RuntimeError('invalid job ID')
    if not set(plan['remove']) <= set(plan['files']):
        raise RuntimeError('invalid removal inventory')
    checkpoint = load(directory / 'checkpoint.json')
    phase = checkpoint['phase']
    if (not resume and phase != 'preview') or (resume and phase not in ('stopping', 'backed-up', 'deleting', 'verified')):
        raise RuntimeError('checkpoint phase does not permit this operation')
    if not resume:
        service_preflight()
        if make_plan() != plan:
            raise RuntimeError('state changed since preview; create a new preview')
        checkpoint = {'phase': 'stopping', 'old_units': {u: migration.unit_state(u) for u in migration.OLD_UNITS}}
        save(directory, checkpoint)
    for unit in migration.OLD_UNITS:
        migration.run('systemctl', 'stop', unit)
    migration.verify_stopped(migration.OLD_UNITS)
    migration.reject_writers(set())
    for unit in migration.OLD_UNITS:
        migration.run('systemctl', 'mask', '--runtime', unit)
    current = validate_remaining(plan)
    if checkpoint['phase'] == 'stopping':
        if current != plan['files']:
            raise RuntimeError('state changed during shutdown')
        config, token_source, _ = migration.translated_config()
        backup = directory / 'metadata'
        backup.mkdir(mode=0o700, exist_ok=True)
        for name in plan['files']:
            parts = PurePosixPath(name).parts
            if name == 'api-token' or (len(parts) == 3 and parts[0] == 'jobs' and parts[2] == 'job.json') or (len(parts) == 4 and parts[:2] == ('store', 'artifacts') and parts[3] == 'artifact.json'):
                destination = backup / name
                destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                shutil.copy2(migration.OLD_STATE / name, destination)
        for source, name in ((migration.OLD_CONFIG, 'legacy-config'), (token_source, 'external-token')):
            if source is not None and source.exists():
                if source.is_symlink():
                    raise RuntimeError('credential/config links require manual cleanup')
                shutil.copy2(source, backup / name)
        migration.sync_backup(directory)
        checkpoint['phase'] = 'backed-up'
        save(directory, checkpoint)
    # Hash and validate retained artifacts before any irreversible deletion.
    for key in plan['keepArtifacts']:
        verify_artifact(key)
    checkpoint['phase'] = 'deleting'
    save(directory, checkpoint)
    for name in plan['remove']:
        path = migration.OLD_STATE / name
        if path.exists():
            if inventory_entry(path) != plan['files'][name]:
                raise RuntimeError('deletion target changed')
            path.unlink()
            sync_directory(path.parent)
    for prefix, keys in (('jobs', plan['removeJobs']), ('store/artifacts', plan['removeArtifacts'])):
        for key in keys:
            path = migration.OLD_STATE / prefix / key
            if path.exists():
                for child in sorted(path.rglob('*'), reverse=True):
                    child.rmdir()
                path.rmdir()
                sync_directory(path.parent)
    current = validate_remaining(plan)
    if set(current) != set(plan['files']) - set(plan['remove']):
        raise RuntimeError('cleanup did not remove every approved file')
    for key in plan['keepArtifacts']:
        verify_artifact(key)
    checkpoint['phase'] = 'verified'
    save(directory, checkpoint)
    for unit, flags in checkpoint['old_units'].items():
        migration.run('systemctl', 'unmask', '--runtime', unit)
        if flags['active']:
            migration.run('systemctl', 'start', unit)
    checkpoint['phase'] = 'complete'
    save(directory, checkpoint)


def inventory_entry(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise RuntimeError('unsafe deletion target')
    return {'size': info.st_size, 'sha256': digest(path), 'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('preview', 'apply', 'resume'))
    parser.add_argument('--plan-dir', type=Path, required=True, help='new private directory outside legacy state')
    parser.add_argument('--maintenance-window', action='store_true')
    args = parser.parse_args()
    if os.geteuid() != 0 or not args.maintenance_window:
        parser.error('root and --maintenance-window required; isolate all clients')
    directory = args.plan_dir.absolute()
    if directory.parent.resolve() != migration.BACKUP_ROOT.resolve() or not directory.name.startswith('mps-cleanup-'):
        parser.error('plan directory must be /var/backups/mps-cleanup-NAME')
    if directory.parent.is_symlink():
        parser.error('backup parent must not be a symlink')
    if migration.OLD_STATE.resolve() == directory.resolve() or migration.OLD_STATE.resolve() in directory.resolve().parents:
        parser.error('plan/metadata backup must be outside legacy state')
    descriptor = os.open(migration.LOCK_FILE, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        # Migration and cleanup cannot overlap or hide an interrupted operation.
        for checkpoint in migration.BACKUP_ROOT.glob('mps-migration-*/checkpoint.json'):
            if load(checkpoint).get('phase') not in ('complete', 'rolled-back', 'preflight'):
                raise RuntimeError('unfinished migration requires recovery first')
        for checkpoint in migration.BACKUP_ROOT.glob('mps-cleanup-*/checkpoint.json'):
            if checkpoint.parent != directory and load(checkpoint).get('phase') not in ('preview', 'complete'):
                raise RuntimeError('unfinished cleanup requires resume first: ' + str(checkpoint.parent))
        if args.operation == 'preview':
            service_preflight()
            plan = make_plan()
            directory.mkdir(mode=0o700)
            (directory / 'plan.json').write_text(json.dumps(plan, indent=2))
            save(directory, {'phase': 'preview'})
            migration.sync_backup(directory)
            sync_directory(directory.parent)
            print(json.dumps({key: value for key, value in plan.items() if key not in ('files', 'credentials')}, indent=2))
            print('Exact per-file IDs, byte counts, modes and checksums: ' + str(directory / 'plan.json'))
        else:
            execute(directory, resume=args.operation == 'resume')
            print('Cleanup verified; original legacy service activity restored. Metadata only: ' + str(directory))


if __name__ == '__main__':
    os.umask(0o077)
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, TypeError, migration.subprocess.CalledProcessError) as error:
        print('Cleanup stopped; do not restart services until recovery/verification: ' + str(error), file=sys.stderr)
        sys.exit(1)
