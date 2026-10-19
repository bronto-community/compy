package distro

// Pinned collector distribution definitions, version v0.163.1 of
// open-telemetry/opentelemetry-collector-releases — the same release the
// bundled otelcol-compy manifest builds from. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.163.1/<asset>
//
// SHA256 values below come from the release's published per-asset
// <asset>.sha256 files. The initial import was independently cross-checked
// by downloading the otlp darwin_arm64 tarball and running `shasum -a 256`
// on it (2026-08-27); it matched exactly. Version bumps rewrite this file
// with fresh values from the same .sha256 assets
// (.github/scripts/bump-collector.py, run by the collector-bump workflow).
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.163.1/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.163.1",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.163.1_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.163.1_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "664047e6ffd98ffaaf90d282bb481ffad46b1e8456f0b2f65e42d627d665d8d3",
			"linux_amd64":  "13e8983077dbfcd2fda38e842262b6bf7539b9310f48d981239989f78264445c",
		},
	},
	{
		Name:    "contrib",
		Version: "0.163.1",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.163.1_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.163.1_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "c25aa7abb438d0474ffec02b0abeb04aeb688ad72e083cc1af0bed1f84dae856",
			"linux_amd64":  "e93e6fd2b59427420a9e8eba5b9e78260df6d21e95283a7298084d2aa07f9df7",
		},
	},
	{
		Name:    "otlp",
		Version: "0.163.1",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.163.1_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.163.1_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "cde9db18369492e034fe5291c7e33682e407de497c1e3c1dc6f2201c1a644b90",
			"linux_amd64":  "ae879fe3646b59bee59b2e3a11937031a288cea4bf9a32c4ecd16cf6a37ca469",
		},
	},
}
