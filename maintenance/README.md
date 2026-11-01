# Opt-in maintenance

Set `maintenanceEnabled: true` locally in the agent YAML configuration and restart
the service. The default is false. Both maintenance kinds require root, Linux
amd64/arm64, systemd, a canonical installation at `/opt/swallow-agent`, and
apt-get, dnf, or yum. Non-Linux builds advertise unsupported.

The initial maintenance-capable agent must be installed manually. Remote upgrades
accept explicit stable release tags (including build metadata), never URLs or
commands. They fetch only this project's GitHub release binary, upgrade script,
and checksums over HTTPS, verify both assets, and require the candidate's
`-maintenance-capable` probe. Checksums share the upstream release trust boundary;
they are not independently signed. Existing upgrade-script layout and downgrade
checks remain in force. Success verifies the running service executable version,
not successful Monitor reporting.

Both operations run in an independent transient systemd service named
`swallow-maintenance-<SHA256(task ID)>.service`. Package operations use fixed argv:
apt-get update, conservative apt-get upgrade, and apt-get check; or dnf/yum
upgrade followed by check. There is no dist upgrade or automatic reboot. Package
maintainer scripts may restart services, and a manual reboot may still be needed.

Unattended policy: commands receive EOF on stdin (Go os/exec connects nil stdin
to `/dev/null`), `DEBIAN_FRONTEND=noninteractive`, and `NEEDRESTART_MODE=l`.
Apt uses `-y` and dpkg `--force-confdef` plus `--force-confold`: prefer the
default configuration-file action, keeping the existing file if no default is
available. Dnf/yum use `-y`. Needrestart only lists services needing restart;
this does not prevent package maintainer scripts themselves restarting services.
Scripts requiring answers without defaults must be handled locally; errors stop
the task and retain output, without automatic retry. Scripts that ignore EOF
may still hang. Package managers are not killed on timeout; a stuck task remains
active and blocks new work. Automatic no-progress alerts are not implemented.

## Disposable Docker verification

`docker_linux_test.go` exercises the production `updateSystem`/`run` functions
against real apt/dpkg using a local fixture repository. It is skipped by default
and requires `SWALLOW_DOCKER_APT_TEST=1` plus Docker detection. Never enable it
in a development container with valuable package state or host system mounts.

Compile the maintenance test binary with `CGO_ENABLED=0 go test -c`, then run it
inside a fresh `debian:bookworm-slim` container with the binary mounted read-only,
`--rm --network none`, the opt-in environment variable, and arguments
`-test.run=^TestDockerUnattendedAPT$ -test.v -test.timeout=45s`. The timeout is
only a disposable-test watchdog, not a production package-manager timeout.

Verified: real upgrade from fixture 1.0 to 2.0 retains a locally edited conffile;
maintainer scripts receive the noninteractive/needrestart environment and EOF;
fixture 3.0 requiring an answer fails with apt exit 100 and script exit 42,
without proceeding to verification. This does not exercise real needrestart
service discovery, debconf questionnaires, RPM managers, or the systemd worker
and Monitor end-to-end flow. The fixture packages require no network access.

`/opt/swallow-agent/maintenance` (0700) holds the copied worker, `state.json`,
`scope` and advisory `lock`. State writes are atomic with file and directory fsync;
all processes use the same lock. Acceptance precedes launch. Output is retained
as bounded lifecycle-phase events (up to roughly 54 KB per task, depending on write chunk
sizes), with an explicit truncation event; final outcome slots are reserved.
The journal retains at most 4096 task IDs and refuses new tasks at capacity rather
than losing deduplication history. Never delete its files during execution.

Events are replayed every approximately three seconds, at most 16 per batch,
until cumulatively acknowledged. Timestamps are Unix seconds. Disconnecting the
Monitor does not cancel execution. On restart an existing worker continues;
missing/uncertain workers produce `unknown` and block new tasks. A still-running
worker may subsequently record its actual outcome. Disk failures block progress
and final-result persistence is retried rather than silently discarded.

The journal is bound to report URL and public key. Changing either while enabled
fails startup until a local operator reconciles the old journal. For unknown tasks
or full history, inspect the transient unit, package-manager state, running agent
version, and journal before any manual recovery. There is intentionally no remote
reset or automatic retry of an uncertain maintenance operation. Disabling the
feature does not kill an already independent worker; re-enable with the same
identity to replay its results.
