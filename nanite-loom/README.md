# Loom integration

Install `loom/v0.1.0` through Nanite's plugin catalog. The plugin requires public
host contract v0.1.7 and plugin-sdk v0.6.1. Provision the database-backed
`loom-curator` durable instance and the `loom-curator`/`loom-weaver` agent profiles
through the host's normal agent APIs before enabling the plugin.

Configure Fragments Engine's callback destination to POST to:

```text
http://127.0.0.1:8090/api/plugins/nanite.loom/curator-wake
```

Adjust the host port to your installation. The previous `/api/loom/curator-wake`
route is retired on host adoption. Convert callback destinations to the plugin
route during that cutover; old routes are not aliased.

The callback body contains fragment identity, not fragment content:

```json
{"generator":"wiki_page","fragment":{"id":"fragment-id","source":"fragments","source_type":"fragment","source_id":"source-id","title":"A finding","canonical_path":"wiki/page"}}
```

The plugin turns that identity into a real prompt asking Curator to fetch and
classify the fragment. It wakes only the reviewed `loom-curator` instance.
A successful response reports queued submission, not completed classification.
The plugin never retries an uncertain wake. Callback bodies are bounded and
unknown or duplicate fields are refused.

Three declared reminder defaults target Loom agents: check-before-answer for
Weaver, plus capture-on-discovery for Weaver and Curator. They use the bounded
host predicate DSL and opt-out-able reminders. Host adoption binds the existing pilot reminders and retains their durable
edits, firing history and deletion intent. A missing old reminder is not
recreated; add any desired new reminder through the host reflex editor. Disabling/unloading the plugin makes
its definitions unavailable to execution. The plugin receives no raw steering
state, owns no execution engine and stores no durable data.

Run `make all` from the repository root. The generated manifest and binary are
headless; no UI bundle is required. Four platform release archives include the
Apache-2.0 license. DataDir and CacheDir remain outside release bundles.
