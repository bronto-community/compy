package distro

// Pinned collector distribution definitions, version v0.160.0 of
// open-telemetry/opentelemetry-collector-releases — the same release the
// bundled otelcol-compy manifest builds from. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.160.0/<asset>
//
// SHA256 values below come from the release's published per-asset
// <asset>.sha256 files. The initial import was independently cross-checked
// by downloading the otlp darwin_arm64 tarball and running `shasum -a 256`
// on it (2026-08-27); it matched exactly. Version bumps rewrite this file
// with fresh values from the same .sha256 assets
// (.github/scripts/bump-collector.py, run by the collector-bump workflow).
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.160.0/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.160.0",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.160.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.160.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "a56143a40a2c205cdd63da4af0249f2534691785c14cdd2a0c532becb4335521",
			"linux_amd64":  "5415b8daf782f17cc463c3e46816abf181a68a04b7bcf98c273c3c204096c743",
		},
	},
	{
		Name:    "contrib",
		Version: "0.160.0",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.160.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.160.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "ceb5309ba16f2587dbef765d54e15c803354d038b0495b0b691e1eb9876d17c9",
			"linux_amd64":  "7bb60c584c241c86261c2b8697cd3725dd8c56691f5ad5d98454eaa005b47b0c",
		},
	},
	{
		Name:    "otlp",
		Version: "0.160.0",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.160.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.160.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "a3ffd1e07a090bf361254755bdc375a81ba0fee8815853663b1e313c090616ca",
			"linux_amd64":  "1b944b4578d45770bada702694370105f1418312dfcbe996fb5f4f84b9de34b3",
		},
	},
}
