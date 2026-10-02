import {fileURLToPath} from "node:url";
import {defineConfig} from "vitest/config";
export default defineConfig({build:{lib:{entry:fileURLToPath(new URL("../ui/index.js",import.meta.url)),formats:["es"],fileName:"index"},outDir:"../../dist/plan-ui-check",rollupOptions:{external:["react"]}},resolve:{dedupe:["react","react-dom"]},test:{environment:"happy-dom",maxWorkers:1}});
