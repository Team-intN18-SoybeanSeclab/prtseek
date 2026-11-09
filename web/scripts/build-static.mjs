import { spawnSync } from "node:child_process";

const result = spawnSync(process.execPath, ["node_modules/next/dist/bin/next", "build"], {
  stdio: "inherit",
  env: { ...process.env, NEXT_EXPORT: "1" },
});
if (result.error) console.error(result.error.message);
process.exit(result.status ?? 1);
