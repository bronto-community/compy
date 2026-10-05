package distro

// Pinned collector distribution definitions, version v0.162.0 of
// open-telemetry/opentelemetry-collector-releases — the same release the
// bundled otelcol-compy manifest builds from. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.162.0/<asset>
//
// SHA256 values below come from the release's published per-asset
// <asset>.sha256 files. The initial import was independently cross-checked
// by downloading the otlp darwin_arm64 tarball and running `shasum -a 256`
// on it (2026-08-27); it matched exactly. Version bumps rewrite this file
// with fresh values from the same .sha256 assets
// (.github/scripts/bump-collector.py, run by the collector-bump workflow).
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.162.0/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.162.0",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.162.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.162.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "53de34c02913f54aa240861e0febf67a6839bc8ecf1bfab83a8ae7d355d30674",
			"linux_amd64":  "f99929987a915d3c6b2c9b15bc4938c5cea903a37a3f49e478024a0fa0772339",
		},
	},
	{
		Name:    "contrib",
		Version: "0.162.0",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.162.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.162.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "d5e11974d2e4adac3cc001a25137aad52f6e77ec3034ba7735cac7c219a5f97a",
			"linux_amd64":  "fcc063749f730f8c21fe29f2d340ff174f5f1c5885bd3156fb6c985a3036fcc3",
		},
	},
	{
		Name:    "otlp",
		Version: "0.162.0",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.162.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.162.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "71075a935b43ea57b7f952609d60e09976faeb75c88b4057470769381f6aa852",
			"linux_amd64":  "062dfdd398ee8dfbce87ec8f9231bd77a994c6aaab3c983a80d94c315d1aad8d",
		},
	},
}
