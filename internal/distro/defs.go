package distro

// Pinned collector distribution definitions, version v0.159.0 of
// open-telemetry/opentelemetry-collector-releases — the same release the
// bundled otelcol-compy manifest builds from. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.159.0/<asset>
//
// SHA256 values below come from the release's published per-asset
// <asset>.sha256 files. The initial import was independently cross-checked
// by downloading the otlp darwin_arm64 tarball and running `shasum -a 256`
// on it (2026-08-27); it matched exactly. Version bumps rewrite this file
// with fresh values from the same .sha256 assets
// (.github/scripts/bump-collector.py, run by the collector-bump workflow).
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.159.0/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.159.0",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.159.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.159.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "267d62245baeb00f78c44ac97036bb27cb99d9217b55875c831fd60b6cb7f309",
			"linux_amd64":  "d56f84c3e7a67c3b8e4f4e25734ec5456be1271fab233a70486ebf1cf181a1e8",
		},
	},
	{
		Name:    "contrib",
		Version: "0.159.0",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.159.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.159.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "7e317b75b1b087ba2150bf95d79e39a394d0d091f1231af6bbebee895d200375",
			"linux_amd64":  "9d589f6349f01179957a2052bc7307a99db2efc971e14e00575941a77122eaaf",
		},
	},
	{
		Name:    "otlp",
		Version: "0.159.0",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.159.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.159.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "990e9d8be19cb77949be3f347c67a213d5b2174213b0c077d62b1a17d11c7057",
			"linux_amd64":  "73d442ba0c041f288bc410fcdd15618a8610767aff383702489bbca503bba82b",
		},
	},
}
