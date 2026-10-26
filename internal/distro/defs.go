package distro

// Pinned collector distribution definitions, version v0.163.2 of
// open-telemetry/opentelemetry-collector-releases — the same release the
// bundled otelcol-compy manifest builds from. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.163.2/<asset>
//
// SHA256 values below come from the release's published per-asset
// <asset>.sha256 files. The initial import was independently cross-checked
// by downloading the otlp darwin_arm64 tarball and running `shasum -a 256`
// on it (2026-08-27); it matched exactly. Version bumps rewrite this file
// with fresh values from the same .sha256 assets
// (.github/scripts/bump-collector.py, run by the collector-bump workflow).
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.163.2/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.163.2",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.163.2_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.163.2_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "56c9c5ab73553f545e3cc4f60373730b8fc262f136e00a6fc13a5ea8107cbe4b",
			"linux_amd64":  "9fbf3101331c88aab4b7f4cfd7c788b252e91e193793e39bd2154971c87f88a7",
		},
	},
	{
		Name:    "contrib",
		Version: "0.163.2",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.163.2_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.163.2_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "ae0dff18926bc3f1e3ef4d1633c4ad4326781f302da2ba5be2ce215ba86e4b9e",
			"linux_amd64":  "cc19dd6a1b85ca694ef16655688e597f693d0e24d2baecdd50e2db597819eeff",
		},
	},
	{
		Name:    "otlp",
		Version: "0.163.2",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.163.2_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.163.2_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "f7da3725549c211128189460bd322610d525e50898dc02126826a114682bea2a",
			"linux_amd64":  "3f32bdb30b969f01c9c7c866d803ca6a08f9f5660bbad8a4f96d49270a0c8547",
		},
	},
}
