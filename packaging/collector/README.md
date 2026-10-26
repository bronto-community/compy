# Updating the bundled collector

Compy pins the binary release in `manifest.yaml` and `internal/distro/defs.go`.
The manifest's individual Go module versions and `builder-version` follow
the upstream release's manifests and Makefile. These versions can differ:
upstream 0.163.2 uses 0.163.0 components/OCB and 1.69.0 confmap providers.

## Compatibility notes for 0.159.0 → 0.163.2

The [0.160.0 contrib release](https://github.com/open-telemetry/opentelemetry-collector-contrib/releases/tag/v0.160.0)
raises the minimum Go version to 1.26 and removes deprecated Kafka auth
fields and client options. Custom Kafka configurations need review before
switching collectors. The [0.162.0 contrib release](https://github.com/open-telemetry/opentelemetry-collector-contrib/releases/tag/v0.162.0)
also changes Prometheus label sanitization defaults. These changes are not
exercised by Compy's shipped OTLP configurations.

Upstream removals of the Mezmo exporter and Wavefront receiver do not
remove any of Compy's selected components. The curated distribution
builds, and the shipped configurations pass validation and integration
tests; this does not certify every user-authored configuration.

## Update and validate

Run the updater from the repository root with a stable binary release:

```sh
python3 .github/scripts/bump-collector.py 0.163.2
python3 -m unittest discover -s .github/scripts -p 'test_*.py' -v
```

The script fetches both upstream component manifests, the upstream OCB
version, and each published download checksum before writing the pins.
Removed components or conflicting upstream pins fail the update for manual
review. It keeps Compy's curated component set; it does not add every
component shipped upstream.

Build into an isolated output directory, using a current stable Go patch
release so binary-only upstream runtime/security fixes are reflected in the
custom build too. `GOTOOLCHAIN=go1.27.3` was used for the October 2026 update;
select a current maintained toolchain when updating later.

```sh
mkdir -p dist/collector
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0 sh packaging/collector/build.sh dist/collector
dist/collector/otelcol-compy --version
OTELCOL_BIN="$PWD/dist/collector/otelcol-compy" go test -tags=integration ./integration/
```

The builder downloads a large module graph and may take several minutes.
Output is `otelcol-compy` plus its version stamp. An explicit output
directory avoids replacing an executable that a local stable instance
might use. A dedicated macOS guest is recommended for interactive testing
when the host has a stable collector. The same build and integration-test
commands can run inside the guest; VM tooling is optional and is not
required by this collector update. Do not activate configurations or
restart the stable host collector incidentally during validation.

Use [the recurring maintenance issue](https://github.com/bronto-community/compy/issues/29)
to request periodic checks from any coding agent. Its runbook covers
discovery, updating, validation, and recording the result. Keep it open
after individual checks; scheduled bump PRs are deferred for now.
Collector update PRs build and test the actual bundled distribution.
Optional automation and the release path are documented
in [.github/workflows/README.md](../../.github/workflows/README.md).
Prebuilt artifact cache keys include both the manifest and OCB pin, using
`cache-key.sh` in both the build workflow and release consumer.

For downloaded `core`, `contrib`, and `otlp` distributions, Compy already
checks upstream periodically and supports `compy distro update --check
contrib` and `compy distro update contrib`. Run mutating commands in the
guest when testing. The bundled `compy` distribution ships with Compy
releases, so tracking upstream and shipping a new bundle are separate
steps. Update checks do not automatically replace a running collector.

The 0.163.2 build was validated on 2026-10-26 with Go 1.27.3: the shipped
configuration integration tests and macOS guest smoke check passed,
including launchd stop/start, all three OTLP signals, the web API, and
native window startup. The host's stable collector remained unchanged.
