package distro

// Pinned collector distribution definitions, version v0.135.0 of
// open-telemetry/opentelemetry-collector-releases. URLs follow the GitHub
// release-asset naming for that repo:
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.135.0/<asset>
//
// SHA256 values below were taken from the release's published
// opentelemetry-collector-releases_<distro>_checksums.txt files and
// independently cross-checked by downloading the darwin_arm64 tarballs and
// running `shasum -a 256` on them (see task-4-report.md for the exact
// commands and output) — both matched exactly.
const releaseBase = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.135.0/"

var defs = []Def{
	{
		Name:    "core",
		Version: "0.135.0",
		Binary:  "otelcol",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol_0.135.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol_0.135.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "47d72b142bb7479101044439fc0bdadcd529f6dbb08f6eb729c8dedb840374ec",
			"linux_amd64":  "bf6e97a6674b8e672350ac954fa230def35c1a25a5f28977348822032e15ec86",
		},
	},
	{
		Name:    "contrib",
		Version: "0.135.0",
		Binary:  "otelcol-contrib",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-contrib_0.135.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-contrib_0.135.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "a6c6b21b85d469b7fcbade017b3e8d39cd88580f3ed5c972542a223771b2f485",
			"linux_amd64":  "43132748eb0effb56b9d508ca789149684bf7ab6ade5d65cd0b22c4d265a30c0",
		},
	},
	{
		Name:    "otlp",
		Version: "0.135.0",
		Binary:  "otelcol-otlp",
		URLs: map[string]string{
			"darwin_arm64": releaseBase + "otelcol-otlp_0.135.0_darwin_arm64.tar.gz",
			"linux_amd64":  releaseBase + "otelcol-otlp_0.135.0_linux_amd64.tar.gz",
		},
		SHA256: map[string]string{
			"darwin_arm64": "aeb17e692e8d3d3fcffa91c92e5937b871bf82558ff4765fcb13766989fe7033",
			"linux_amd64":  "435b3931dc2243fd99234cc956117079731ce3fe81782c57fbca85d5f51278cf",
		},
	},
}
