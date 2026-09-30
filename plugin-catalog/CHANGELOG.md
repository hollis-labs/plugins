# Changelog

All notable changes to nanite-plugins-catalog are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This repository has no tagged releases; it is a catalog build source, and the published catalog is produced by CI and a maintainer-run publish step. This file was backfilled from the git history.

## [Unreleased]

### Added

- Project `AGENTS.md` describing the layout, the catalog signing and trust model, and the production publish boundary.

## 2026-04-13

### Added

- Initial commit: the v1 catalog output schema (`catalog.schema.v1.json`), the catalog builder (`scripts/build-catalog.go`), the detached Ed25519 signing pipeline (`scripts/sign-catalog.sh`), the seed plugin list (`plugins.yaml`), and the CI build workflow.
