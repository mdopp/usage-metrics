# Changelog

## [0.2.1](https://github.com/mdopp/usage-metrics/compare/usage-metrics-v0.2.0...usage-metrics-v0.2.1) (2026-08-26)


### Bug Fixes

* **ci:** publish the container image to ghcr so the template can install ([#11](https://github.com/mdopp/usage-metrics/issues/11)) ([0ecd417](https://github.com/mdopp/usage-metrics/commit/0ecd41703576dc91b13c13d1c363d3471c90b5e9))

## [0.2.0](https://github.com/mdopp/usage-metrics/compare/usage-metrics-v0.1.0...usage-metrics-v0.2.0) (2026-08-26)


### Features

* **ingest:** POST /ingest with WAL-mode SQLite counter storage ([#6](https://github.com/mdopp/usage-metrics/issues/6)) ([7f80361](https://github.com/mdopp/usage-metrics/commit/7f8036193364e6ba310efa9ce2d3d75cfe4ea97d)), closes [#2](https://github.com/mdopp/usage-metrics/issues/2)
* **retention:** drop counters older than a configurable window ([#7](https://github.com/mdopp/usage-metrics/issues/7)) ([2d5fc9c](https://github.com/mdopp/usage-metrics/commit/2d5fc9c431a3caf349ea41fd134e5b4f4026850d)), closes [#3](https://github.com/mdopp/usage-metrics/issues/3)
* scaffold usage-metrics service ([96e1cde](https://github.com/mdopp/usage-metrics/commit/96e1cdea8858cb477d63cbd9fc1a8aba10d29238))
* **summary:** GET /summary with counts per app × event over the last N days ([#8](https://github.com/mdopp/usage-metrics/issues/8)) ([ae1ade3](https://github.com/mdopp/usage-metrics/commit/ae1ade3ad5a25437ec8df87a49b4c2dd3faa157c)), closes [#4](https://github.com/mdopp/usage-metrics/issues/4)
* **template:** ServiceBay template with service-token auth on the counter endpoints ([#9](https://github.com/mdopp/usage-metrics/issues/9)) ([0e327c6](https://github.com/mdopp/usage-metrics/commit/0e327c6cbce66be2e40c7b112d0afc527c18df55)), closes [#5](https://github.com/mdopp/usage-metrics/issues/5)
