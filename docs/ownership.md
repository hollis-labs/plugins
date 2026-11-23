# Plugin ownership and compatibility

This repository is canonical for shared portfolio plugins, including all ten `nanite-*` plugins. Host-specific plugins keep their host prefix in the flat layout (`nanite-*`, `cerberus-*`, `tangent-*`); shared connectors can retain neutral names. A prefix is naming, not the compatibility contract.

`plugin.json` declares kind and hosts. `plugin-compatibility.json` records those declarations and each module's Hollis Labs dependency requirements. Requirements are build inputs, not proof of tested host versions. Declarations do not install a plugin or grant permissions.

## Nanite duplication

All ten Nanite plugins already arrived here with history. Comparison confirmed 149 non-license files match the former aggregate after only importer module/SDK path rewrites; checksum files are unchanged. Original licenses, source history, root build/release tooling and release assets are preserved offline. The old aggregate is reference/distribution history, not a second development source.

The former aggregate's build/release bundler, contract-only dependency guard and catalog-update workflow have not been adapted here. Existing plugin-catalog source is preserved. No new release, host adoption, dependency-pin rewrite or user-data migration is implied. Historical downloads remain available pending a verified distribution cutover.

## Validation and future releases

Current CI plans changed plugins independently, runs layout/descriptor/public-history guards and module vet/race checks. This ownership inventory does not prove older SDK pins resolve or plugins work with current Nanite, Surface or Exo. Preserve the public host contract and host-internal import boundary when adapting release tooling. Before publishing here, restore equivalent manifest/archive/checksum/dependency/catalog checks and record concrete tested host/contract versions.
