package distro

// Pinned collector distribution definitions, version v0.161.0 of
// open-telemetry/opentelemetry-collector-releases — the same release the
// bundled otelcol-compy manifest builds from. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.161.0/<asset>
//
// SHA256 values below come from the release's published per-asset
// <asset>.sha256 files. The initial import was independently cross-checked
// by downloading the otlp darwin_arm64 tarball and running `shasum -a 256`
// on it (2026-08-27); it matched exactly. Version bumps rewrite this file
// with fresh values from the same .sha256 assets
// (.github/scripts/bump-collector.py, run by the collector-bump workflow).
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.161.0/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.161.0",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.161.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.161.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "b6816373a0f6d6e21f7cecb92521a3c1c2e42ae3cb992c647e17380de5265b5d",
			"linux_amd64":  "6c2af0731a53691e3771d82a4b71e2941a46de27af191e40c697b6ed8643e50e",
		},
	},
	{
		Name:    "contrib",
		Version: "0.161.0",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.161.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.161.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "ccc0cf5de5242adcaedc7b5aebed43a1dc56aa2dc7de6ebc495d5db60512d34c",
			"linux_amd64":  "778c689efa681ff6e4722ce9f66b9b7f57c3ba009ab2e2b43dc2e0315862c731",
		},
	},
	{
		Name:    "otlp",
		Version: "0.161.0",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.161.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.161.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "d1c7e528f17bf395e94d2ce7921931de2a1c2f86812f8beeae42a3c5db8a5919",
			"linux_amd64":  "d1dede3416521712afe494a63eb0ec0222be83cb6b7454e8926b02e24a67c64a",
		},
	},
}
