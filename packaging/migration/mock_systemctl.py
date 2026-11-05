#!/usr/bin/python3
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0
"""Disposable container harness only. Never use as a real service controller."""
import json
import os
from pathlib import Path
import pwd
import shlex
import signal
import subprocess
import sys
import time

root = Path('/tmp/mock-systemctl')
root.mkdir(exist_ok=True)
statefile = root / 'state.json'
state = json.loads(statefile.read_text()) if statefile.exists() else {}
args = [arg for arg in sys.argv[1:] if arg not in ('--system', '--quiet', '--runtime')]
command = args.pop(0)
unit = args[0] if args else ''
record = state.setdefault(unit, {'active': False, 'enabled': False, 'masked': False, 'pid': 0})
code = 0
if command in ('start', 'restart'):
    if record['masked']:
        sys.exit(1)
    if unit == 'mps.service':
        if (root / 'fail-start').exists():
            (root / 'fail-start').unlink()
            sys.exit(1)
        env = dict(os.environ)
        for line in Path('/etc/default/mps').read_text().splitlines():
            if line.strip() and not line.lstrip().startswith('#'):
                key, value = shlex.split(line)[0].split('=', 1)
                env[key] = value
        user = pwd.getpwnam('mps')
        def demote():
            os.setgid(user.pw_gid)
            os.setuid(user.pw_uid)
        log = (root / 'daemon.log').open('ab')
        proc = subprocess.Popen(['/usr/bin/mpsd', 'serve'], env=env, stdout=log, stderr=log, preexec_fn=demote, start_new_session=True)
        record['pid'] = proc.pid
    record['active'] = True
elif command == 'stop':
    if record['pid']:
        try:
            os.kill(record['pid'], signal.SIGTERM)
        except ProcessLookupError:
            pass
        time.sleep(.25)
    record.update(active=False, pid=0)
elif command in ('enable', 'disable'):
    record['enabled'] = command == 'enable'
elif command in ('mask', 'unmask'):
    for name in args:
        state.setdefault(name, {'active': False, 'enabled': False, 'masked': False, 'pid': 0})['masked'] = command == 'mask'
elif command == 'is-active':
    code = 0 if record['active'] else 3
elif command == 'is-enabled':
    print('masked' if record['masked'] else 'enabled' if record['enabled'] else 'disabled')
    code = 0 if record['enabled'] else 1
elif command == 'show':
    prop = next(arg.split('=', 1)[1] for arg in args if arg.startswith('--property='))
    values = {'DropInPaths': '', 'ControlGroup': '', 'FragmentPath': '/lib/systemd/system/' + unit if Path('/lib/systemd/system', unit).exists() else '',
              'MainPID': str(record['pid']), 'ActiveState': 'active' if record['active'] else 'inactive'}
    print(values[prop])
elif command != 'daemon-reload':
    sys.exit('unsupported mock command: ' + command)
statefile.write_text(json.dumps(state))
sys.exit(code)
