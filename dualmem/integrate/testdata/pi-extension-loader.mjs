import { pathToFileURL } from "node:url";

const [extensionPath, loaderPath] = process.argv.slice(2);
const { loadExtensions } = await import(pathToFileURL(loaderPath).href);
const loaded = await loadExtensions([extensionPath], process.cwd());
if (loaded.errors.length > 0 || loaded.extensions.length !== 1) {
  throw new Error(JSON.stringify(loaded.errors));
}

const handlers = loaded.extensions[0].handlers.get("tool_call") ?? [];
if (handlers.length !== 1) {
  throw new Error(`tool_call handlers=${handlers.length}`);
}

const notifications = [];
await handlers[0]({ toolName: "read", input: { path: "fixture.go" } }, {
  cwd: process.cwd(),
  ui: {
    notify(message, level) {
      notifications.push({ message, level });
    },
  },
});
process.stdout.write(JSON.stringify(notifications));
