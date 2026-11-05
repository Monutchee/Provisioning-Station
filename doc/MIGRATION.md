# Migrate legacy Provisioning Station installations

The rename is an explicit migration. No old executable names or `MNC_*`
configuration aliases are accepted. Artifact schema `mnc-station-artifact` v2
and `/api/v1` remain unchanged.

## Debian maintenance window

Reserve a maintenance window and isolate every browser, API, `mnc`, and other
automation client. Finish or explicitly cancel queued/running jobs first. The
operator must keep those clients isolated until migration or rollback finishes;
the script's persisted-job checks do not prevent a new client submitting work
between a check and service shutdown. No hardware job is run by the script.

Keep the exact installed legacy `mnc-station` `.deb`. The script accepts it via
`--old-deb` or finds a matching package/version/architecture in
`/var/cache/apt/archives`. Missing rollback media blocks the migration before
services are changed. Copy both `.deb` files to trusted root-controlled paths.
Install Python 3 before starting. Use the complete `packaging/migration/`
directory from the repository, or `migration/` from the Linux release archive.
Keep `migrate.py` beside its `legacy-units/` companion directory; copying only
the script is insufficient.

```bash
sudo python3 packaging/migration/migrate.py \
  --new-deb /trusted/path/mps_1.2.3_amd64.deb \
  --old-deb /trusted/path/mnc-station_1.2.2_amd64.deb \
  --maintenance-window
```

A root-private process lock prevents concurrent migrations. The script checks
for active persisted jobs, validates simple legacy environment
assignments, stops the legacy pair, and rechecks jobs. It writes a root-private
backup under `/var/backups/mps-migration-*` with exact rollback media, old state,
config, service flags, and phase checkpoints. It masks services during package
changes, removes the old package, installs the supplied new package, copies
state to `/var/lib/mps`, translates config to `/etc/default/mps`, and sets `mps`
ownership/private permissions before starting the service. Existing legacy
state is copied and retained. A configured external token is copied into the
new managed token file; token values are never printed.

Custom legacy state directories, service overrides/drop-ins, unmanaged hardware
processes, existing new state/config, links/special files
in legacy state, or complex shell configuration require a manual migration.
The tool refuses them before stopping services. Keep backups private: they
contain API tokens and hardware job records.

On a failure after service mutation, the script removes the new package,
preserves failed new state/config in the backup, reinstalls the exact old
package, restores the old config and original service active/enabled states,
and reports the checkpoint. A `rollback-incomplete` checkpoint needs operator
attention: inspect its failed command list and restore using the private
`old.deb`, `old-config`, and `old-state`. Do not delete backup media until the
new service has been verified. Abrupt power loss cannot run automatic rollback;
use the recorded phase and backup to finish or undo the interrupted operation.

Verify `systemctl status mps mps-hw-server` and `mps status`, then reconnect the
clients. Artifacts and historical jobs should remain visible. The old state
and account are deliberately retained for operator-controlled cleanup later.

## Windows

Stop the old foreground application and identify the actual user's old state
under `%AppData%\Monutchee\Provisioning-Station`. An elevated MSI cannot choose
that user safely. Back up that state and its token explicitly. Install the new
MSI, stop `mps` from an administrator terminal, copy the backed-up contents into
`%ProgramData%\Monutchee\MPS`, apply the directory's protected inherited ACL to
the copied files, and translate machine configuration to `MPS_*`. Restart `mps`
and verify retained artifacts/jobs before reconnecting clients. Do not grant
ordinary users access to the private token directory. Use administrator token
administration or an explicit client token file.
