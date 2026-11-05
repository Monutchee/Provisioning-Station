#!/usr/bin/env bash
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0
# Run inside a disposable container with /check containing new.deb/old.deb.
# Build /check/fixture with: go build -o fixture ./packaging/migration/testfixture
set -Eeuo pipefail
install -m 0755 /check/mock_systemctl.py /usr/local/bin/systemctl
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d
chmod 0755 /usr/sbin/policy-rc.d
dpkg --install /check/old.deb >/tmp/old-install.log
printf 'MNC_STATION_HTTP_LISTEN=0.0.0.0:18042\nMNC_STATION_TOKEN=preserved-token-0123456789#suffix\n' > /etc/default/mnc-station
printf 'preserved-token-0123456789#suffix\n' > /var/lib/mnc-station/api-token
/check/fixture seed /var/lib/mnc-station
/check/fixture verify /var/lib/mnc-station
/check/fixture seed-old /var/lib/mnc-station
python3 - <<'SEED'
import json
from pathlib import Path
root = Path('/var/lib/mnc-station')
artifacts = {p.name: json.loads((p/'artifact.json').read_text()) for p in (root/'store/artifacts').iterdir()}
# The fixture image contents identify the obsolete artifact without relying on directory order.
obsolete = next(key for key in artifacts if (root/'store/artifacts'/key/'payload/tftp/Image').read_bytes() == b'obsolete kernel\n')
retained = next(key for key in artifacts if key != obsolete)
for index in range(35):
    name = 'retained' if index == 34 else f'history{index:02}'
    directory = root/'jobs'/name
    directory.mkdir(parents=True)
    (directory/'job.json').write_text(json.dumps({'id': name, 'state': 'succeeded', 'createdUtc': f'2026-01-01T00:00:{index:02}Z', 'request': {'artifactId': obsolete if index < 5 else retained}}))
    (directory/'serial.log').write_text('retained transcript\n')
SEED
python3 /check/cleanup.py preview --plan-dir /var/backups/mps-cleanup-fixture --maintenance-window
python3 /check/cleanup.py apply --plan-dir /var/backups/mps-cleanup-fixture --maintenance-window
/check/fixture verify /var/lib/mnc-station
python3 - <<'VERIFY'
import json
from pathlib import Path
root = Path('/var/lib/mnc-station')
assert len(list((root/'jobs').iterdir())) == 30
assert not (root/'jobs/history00').exists()
plan = Path('/var/backups/mps-cleanup-fixture')
assert json.loads((plan/'checkpoint.json').read_text())['phase'] == 'complete'
assert not list((plan/'metadata').rglob('artifact.tar.gz'))
assert (plan/'metadata/jobs/history00/job.json').exists()
VERIFY
printf 'retained serial transcript\n' > /var/lib/mnc-station/jobs/retained/serial.log
# Inject service startup failure after package install; automatic rollback must
# reinstall old package and preserve its state, token, and original flags.
touch /tmp/mock-systemctl/fail-start
if python3 /check/migrate.py --new-deb /check/new.deb --old-deb /check/old.deb --maintenance-window; then
    echo 'Injected service failure unexpectedly succeeded' >&2
    exit 1
fi
test "$(dpkg-query -W -f='${Status}' mnc-station)" = 'install ok installed'
test "$(cat /var/lib/mnc-station/api-token)" = preserved-token-0123456789#suffix
python3 - <<'PY'
import json
from pathlib import Path
records = [json.loads(p.read_text()) for p in Path('/var/backups').glob('mps-migration-*/checkpoint.json')]
assert records[-1]['phase'] == 'rolled-back', records
PY
# A second run succeeds and verifies the real daemon's authenticated API and
# historical job IDs; no hardware tools are installed or invoked.
python3 /check/migrate.py --new-deb /check/new.deb --old-deb /check/old.deb --maintenance-window
test "$(dpkg-query -W -f='${Status}' mps)" = 'install ok installed'
test "$(stat -c %U:%G /var/lib/mps)" = mps:mps
test "$(stat -c %a /var/lib/mps)" = 700
test "$(stat -c %a /var/lib/mps/api-token)" = 600
test "$(cat /var/lib/mps/api-token)" = preserved-token-0123456789#suffix
python3 - <<'PY'
import json
from pathlib import Path
records = [json.loads(p.read_text()) for p in Path('/var/backups').glob('mps-migration-*/checkpoint.json')]
assert any(r['phase'] == 'complete' for r in records), records
PY
/check/fixture verify /var/lib/mps
cmp /var/lib/mnc-station/jobs/retained/serial.log /var/lib/mps/jobs/retained/serial.log
systemctl stop mps.service
dpkg --remove mps >/tmp/new-remove.log
test -f /var/lib/mps/api-token
printf 'Disposable package install/migration/rollback/remove passed (mock systemctl, real HTTP daemon).\n'
