# Manual workflow validation

## macOS arm64, November 16, 2026

The owner ran these checks from the host terminal using the installed `kiu dev`
source build. The implementing agent inspected the resulting local artifacts.
These are targeted manual checks, not automated CI or full application acceptance.
Both projects used the default host user's pool with capacity one. Kiu ran on the
host, outside Docker; the existing Calculyo Rails `app` service was already running.

### Local Flutter and Docker Rails

Commands ran from their respective Calculyo project directories:

```sh
kiu --name "Flutter — targeted test" flutter test test/flutter_layout_compatibility_test.dart
kiu --name "Rails — targeted test" docker compose exec -T app env RAILS_ENV=test bin/rails test test/lib/app/data_paths_test.rb
```

The harness used unique job labels and a wrapper around each command to record
child start/end times. The first wrapper retained its slot after its real test
finished until the harness released it, allowing a reliable contention check even
for short tests. The waiting child's start marker was absent before release.

| Scenario | Result |
| --- | --- |
| Flutter holds the slot; Rails waits | Both tests exited 0; Rails started after the first wrapper ended |
| Rails holds the slot; Flutter waits | Both tests exited 0; Flutter started after the first wrapper ended |

Status snapshots showed the expected named running and waiting jobs. Recorded
child lifetimes did not overlap in either direction. This establishes shared-slot
behavior for these commands across two repositories, including a Docker client.
It does not establish full Rails CI, Flutter build or device-test coverage.

### Controlled Docker cancellation

A separate probe created one short-lived Ruby process inside the existing `app`
container. It wrote a heartbeat to its own unique temporary directory and had a
90-second maximum lifetime. Cleanup used that probe's token-checked stop marker;
no application tests, shared services or unrelated processes were terminated.

| Check | Observed result |
| --- | --- |
| Cancel a waiting Kiu invocation with SIGINT | Exit 130; its child never started |
| Cancel the active Kiu Docker invocation with SIGINT | Exit 130; the next queued host command completed with exit 0 |
| Check the original container process after local cancellation | Heartbeat advanced while the host slot was free |
| Stop the probe through its private stop marker | Probe termination confirmed |

**Container work can outlive cancellation of the local Docker client.** Kiu then
releases its host-client slot, so the next admitted job can overlap that remaining
container work. Before retrying an interrupted Docker job, identify, stop and
verify the specific work inside the container. Do not infer termination from Kiu's
exit code or an empty host queue, and do not stop unrelated containers/services.

This result confirms the documented execution boundary; it does not provide a
container-aware cancellation guarantee. Other Docker commands, engine versions
and platforms may behave differently.

## Remaining coverage

- Equivalent real-workflow checks on Linux.
- Full Docker/Rails CI and Flutter builds/device integration tests.
- Native amd64 coverage and automated macOS/Linux CI.
- Broader Docker lifecycle behavior; the host process-group policy does not
  supervise processes in a container's separate process namespace.
