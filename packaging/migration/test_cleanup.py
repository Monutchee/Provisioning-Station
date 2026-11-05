#!/usr/bin/env python3
"""Cleanup tests use only disposable state and mocked service control."""
import hashlib
import io
import json
import os
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
import cleanup
import migrate


class CleanupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / 'state'
        self.state.mkdir()
        self.patch('OLD_STATE', self.state)
        self.patch('OLD_CONFIG', self.root / 'config')
        self.patch('BACKUP_ROOT', self.root)
        self.patch('LOCK_FILE', self.root / 'lock')
        patch.object(cleanup, 'private').start()
        patch.object(cleanup, 'service_preflight').start()
        self.services = patch.object(migrate, 'run').start()
        self.addCleanup(patch.stopall)
        patch.object(migrate, 'verify_stopped').start()
        patch.object(migrate, 'reject_writers').start()
        patch.object(migrate, 'unit_state', return_value={'active': True, 'enabled': True}).start()
        self.keep = self.artifact(b'keep')
        self.remove = self.artifact(b'remove')
        for index in range(35):
            path = self.state / 'jobs' / f'job{index:02}'
            path.mkdir(parents=True)
            (path / 'job.json').write_text(json.dumps({'id': path.name, 'state': 'succeeded',
                'createdUtc': f'2026-01-01T00:00:{index:02}Z',
                'request': {'artifactId': self.remove if index < 5 else self.keep}}))
            (path / 'serial.log').write_bytes(b'private transcript')
        (self.state / 'api-token').write_bytes(b'preserve secret')
        (self.state / 'api-token').chmod(0o600)
        self.directory = self.root / 'mps-cleanup-test'

    def patch(self, key, value):
        patch.object(migrate, key, value).start()
        self.addCleanup(patch.stopall)

    def artifact(self, data, signed=False):
        descriptor = {'size': len(data), 'sha256': hashlib.sha256(data).hexdigest(), 'mode': '0755'}
        manifest = {'schema': 'mnc-station-artifact', 'formatVersion': 2,
                    'artifact': dict(name='fixture', vendor='xilinx', operation='jtag-boot', product='fixture', machine='fixture', version='1.0', buildId='fixture', createdUtc='2026-01-01T00:00:00Z'),
                    'executor': {'type': 'xilinx-xsdb', 'entrypoint': 'loader'}, 'files': {'loader': descriptor}}
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode='w:gz') as archive:
            entries = [('manifest.json', json.dumps(manifest).encode(), 0o644), ('loader', data, 0o755)]
            if signed:
                entries.insert(1, ('manifest.sig', b'signature fixture', 0o644))
            for name, payload, mode in entries:
                info = tarfile.TarInfo(name)
                info.size, info.mode = len(payload), mode
                archive.addfile(info, io.BytesIO(payload))
        data_archive = stream.getvalue()
        key = hashlib.sha256(data_archive).hexdigest()
        root = self.state / 'store/artifacts' / key
        (root / 'payload').mkdir(parents=True)
        (root / 'artifact.tar.gz').write_bytes(data_archive)
        (root / 'payload/manifest.json').write_text(json.dumps(manifest))
        (root / 'payload/loader').write_bytes(data)
        (root / 'payload/loader').chmod(0o755)
        if signed:
            (root / 'payload/manifest.sig').write_bytes(b'signature fixture')
        (root / 'artifact.json').write_text(json.dumps({'id': key, 'manifest': manifest, 'hasSignature': signed}))
        return key

    def preview(self):
        self.directory.mkdir(mode=0o700)
        (self.directory / 'plan.json').write_text(json.dumps(cleanup.make_plan()))
        cleanup.save(self.directory, {'phase': 'preview'})

    def test_exact_retention_and_metadata_only_backup(self):
        self.preview()
        plan = cleanup.load(self.directory / 'plan.json')
        self.assertEqual(len(plan['removeJobs']), 5)
        self.assertEqual(plan['removeArtifacts'], [self.remove])
        self.assertEqual(plan['deleteBytes'], sum(plan['files'][n]['size'] for n in plan['remove']))
        cleanup.execute(self.directory)
        self.assertEqual(len(list((self.state / 'jobs').iterdir())), 30)
        self.assertFalse((self.state / 'store/artifacts' / self.remove).exists())
        self.assertEqual((self.state / 'api-token').read_bytes(), b'preserve secret')
        self.assertFalse(list((self.directory / 'metadata').rglob('artifact.tar.gz')))
        self.assertTrue((self.directory / 'metadata/jobs/job00/job.json').exists())
        self.assertEqual(cleanup.load(self.directory / 'checkpoint.json')['phase'], 'complete')

    def test_signed_artifact_and_created_order(self):
        signed = self.artifact(b'signed', signed=True)
        cleanup.verify_artifact(signed)
        path = self.state / 'jobs/job00/job.json'
        record = cleanup.load(path)
        record['finishedUtc'] = '2030-01-01T00:00:00Z'
        path.write_text(json.dumps(record))
        self.assertIn('job00', cleanup.make_plan()['removeJobs'])

    def test_payload_json_not_backed_up(self):
        # Unknown files are removable with an obsolete artifact, but never copied
        # into the supposedly compact metadata backup just because of extension.
        path = self.state / 'store/artifacts' / self.remove / 'payload/large.json'
        path.write_text('x' * 10000)
        self.preview()
        cleanup.execute(self.directory)
        self.assertFalse(list((self.directory / 'metadata').rglob('large.json')))

    def test_changed_config_during_stop_refused(self):
        migrate.OLD_CONFIG.write_text('MNC_STATION_TOKEN=before\n')
        self.preview()
        def stop(*args):
            if args[:2] == ('systemctl', 'stop'):
                migrate.OLD_CONFIG.write_text('MNC_STATION_TOKEN=after\n')
        self.services.side_effect = stop
        with self.assertRaisesRegex(RuntimeError, 'credentials/config changed'):
            cleanup.execute(self.directory)
        self.assertTrue((self.state / 'jobs/job00').exists())

    def test_changed_preview_refused_before_stop(self):
        self.preview()
        (self.state / 'jobs/job00/serial.log').write_bytes(b'changed')
        with self.assertRaisesRegex(RuntimeError, 'changed since preview'):
            cleanup.execute(self.directory)
        self.services.assert_not_called()

    def test_active_and_malformed_records_refused(self):
        path = self.state / 'jobs/job00/job.json'
        for state in ('running', 'queued', 'unknown'):
            record = cleanup.load(path)
            record['state'] = state
            path.write_text(json.dumps(record))
            with self.assertRaisesRegex(RuntimeError, 'active/unknown'):
                cleanup.make_plan()
        path.write_text('{"id":"job00","id":"duplicate"}')
        with self.assertRaisesRegex(RuntimeError, 'duplicate'):
            cleanup.make_plan()

    def test_old_request_identity_refused(self):
        path = self.state / 'jobs/job00/job.json'
        record = cleanup.load(path)
        record['request']['requestId'] = 'a' * 32
        path.write_text(json.dumps(record))
        with self.assertRaisesRegex(RuntimeError, 'identity requires manual migration'):
            cleanup.make_plan()
        self.services.assert_not_called()

    def test_links_and_interrupted_import_refused(self):
        link = self.state / 'link'
        link.symlink_to(self.root)
        with self.assertRaisesRegex(RuntimeError, 'unsupported state'):
            cleanup.make_plan()
        link.unlink()
        (self.state / 'store/tmp').mkdir()
        (self.state / 'store/tmp/unfinished').touch()
        with self.assertRaisesRegex(RuntimeError, 'interrupted import'):
            cleanup.make_plan()

    def test_corrupt_retained_artifact_never_deletes(self):
        path = self.state / 'store/artifacts' / self.keep / 'payload/loader'
        path.write_bytes(b'corrupt')
        self.preview()
        with self.assertRaisesRegex(RuntimeError, 'checksum/size/mode'):
            cleanup.execute(self.directory)
        self.assertTrue((self.state / 'jobs/job00').exists())
        self.assertFalse(any(call.args[:2] == ('systemctl', 'start') for call in self.services.call_args_list))

    def test_interrupted_unlink_resumes(self):
        self.preview()
        original = Path.unlink
        count = 0
        def interrupted(path, *args, **kwargs):
            nonlocal count
            if self.state in path.parents:
                count += 1
                if count == 3:
                    raise OSError('power loss')
            return original(path, *args, **kwargs)
        with patch.object(Path, 'unlink', interrupted):
            with self.assertRaisesRegex(OSError, 'power loss'):
                cleanup.execute(self.directory)
        self.assertEqual(cleanup.load(self.directory / 'checkpoint.json')['phase'], 'deleting')
        cleanup.execute(self.directory, resume=True)
        self.assertEqual(len(list((self.state / 'jobs').iterdir())), 30)


if __name__ == '__main__':
    unittest.main()
