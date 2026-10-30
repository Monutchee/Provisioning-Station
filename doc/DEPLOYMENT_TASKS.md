# Deployment tasks

Station accepts optional `deploymentTasks` on `POST /api/v1/jobs`. Clients must
check `/api/v1/capabilities` before uploading/submitting a task-bearing request.
The `deploymentTasks` capability advertises supported `versions`, `phases`, and
registered `tasks`, including flat parameter descriptors and `timeoutSeconds`.
Unsupported tasks must stop deployment, never silently disappear. Requests
without tasks keep their existing behavior and remain compatible with older
Station versions.

The production binary starts with an empty trusted registry. No TPM operation
is implied: its device-specific implementation and contract must be defined
separately. Task implementations are compiled Go code registered at Station
startup in `cmd/mnc-station/main.go`; callers cannot register implementations,
select executable paths, or provide shell commands. Vendor-neutral task behavior
belongs in `internal/tasks`; hardware policy belongs in its owning adapter.

A synthetic request fragment illustrates the wire format. `example.prepare`
is illustrative and is not a production task:

```json
{
  "deploymentTasks": {
    "version": 1,
    "beforeBoot": [
      {"id": "prepare", "task": "example.prepare", "parameters": {"mode": "test"}}
    ],
    "onSuccess": [],
    "onFailure": [],
    "onCancel": []
  }
}
```

A request may contain at most 64 invocations total, with IDs unique across all
phases. Parameter descriptors support `string`, `boolean`, `integer`, and
`number`, optional typed enums, and required fields. Unknown parameters, nested
values, nulls, unavailable task IDs, and unsupported versions are rejected before
queueing. Integer parameters accept integral JSON numbers within
`[-9007199254740991, 9007199254740991]`. Descriptor checks always run before any
adapter-specific semantic validation. Every task has a positive finite timeout
of at least one nanosecond and at most 3600 seconds, supplied by trusted registry
configuration, not the job request.

## Execution and outcomes

Tasks execute sequentially and fail fast within each phase. Station's single
job worker stays occupied until all selected tasks finish, including cleanup.
This serializes Station deployment jobs; it is not a cross-process hardware
lease. The Xilinx runner invokes `beforeBoot` after TFTP startup immediately
before XSDB. A preparation failure prevents XSDB execution.

After deployment and serial-capture supervision finish, Station selects exactly
one outcome phase: `onSuccess`, `onFailure`, or `onCancel`. Failure tasks have
fresh contexts and their own task timeouts, allowing cleanup after the boot
timeout. A new cancellation still stops failure tasks. Success tasks respect
job cancellation. Cancellation during an already-selected success/failure phase
stops later task launches and ends the job as `canceled`; it does not select a
second phase. Errors in success tasks fail the overall job; errors in failure
or cancellation tasks supplement the primary outcome and never trigger another
outcome phase.

`onCancel` runs only for a started job whose primary deployment outcome is
cancellation. The whole phase shares one fresh five-second budget, further
bounded by individual task timeouts. Queued cancellation starts no tasks.
A cancellation hook failure does not change `canceled` to `failed`.

The existing `--job-timeout` (default ten minutes) bounds the Xilinx boot phase,
including its preparation tasks. Outcome task timeouts are separate. A job
remains `running` until outcome tasks finish. Its `deploymentOutcome` preserves
the primary outcome and `deploymentError` retains its error even if a later
cancellation ends the overall job, while `taskResults`, `taskErrors`, and ordered job events
record task execution. Task errors exposed by the framework are sanitized;
trusted implementations must avoid logging credentials or sensitive parameters.

Tasks must honor context cancellation and join any child work before returning.
Execution stays synchronous: Station does not abandon a timed-out goroutine and
start another hardware job while prior work still runs. This cooperative
requirement is part of the trusted adapter contract.

Restart never replays deployment tasks. Previously queued/running jobs become
failed, with any running task result marked `interrupted`. Failure/cancellation
hooks do not run during restart recovery. Clients inspect persisted results
before deciding whether a new deployment is appropriate.

## Validation

Synthetic tests cover schema/descriptor checks, immutable snapshots, timeout
joining, phase order, preboot failure, terminal outcomes, cancellation during
outcome tasks, the shared cancellation budget, serialization, restart without
replay, capability discovery, and API rejection. No physical devices are used.

Validated with `go test ./...`, `go test -race ./...`, and `go vet ./...`,
plus `go build ./cmd/mnc-station` for Linux amd64 and Windows amd64. All passed.
API and runner tests use synthetic loopback TCP/UDP services; no hardware or
private product evidence was accessed. The production registry remains empty,
and trusted task implementations must cooperate with context cancellation.
