#!/usr/bin/env node
import { existsSync, readFileSync, writeFileSync, appendFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const getArg = (name) => process.argv.find((arg) => arg.startsWith(`${name}=`))?.split("=")[1];
const profile = getArg("--user-data-dir");
const portArg = getArg("--remote-debugging-port");
const previous = existsSync(new URL("attempts.jsonl", import.meta.url))
  ? readFileSync(new URL("attempts.jsonl", import.meta.url), "utf8").trimEnd().split("\n").map(JSON.parse) : [];
const n = previous.length + 1;
const scratch = resolve(tmpdir(), `fake-browser-${process.pid}`);
writeFileSync(scratch, "");
appendFileSync(new URL("attempts.jsonl", import.meta.url), JSON.stringify({
  profile, portArg, pid: process.pid, scratch,
  previousProfilesGone: previous.every((entry) => !existsSync(entry.profile)),
}) + "\n");
const modes = JSON.parse(readFileSync(new URL("modes.json", import.meta.url), "utf8"));

for (let i = 1; i <= 20; i++) process.stderr.write(`diagnostic line ${i} from attempt ${n}\n`);
const mode = modes[n - 1];
if (!mode || mode === "exit") process.exit(9);
if (mode === "hang") {
  // No DevTools port. The parent must kill this process, not just stop polling.
  setInterval(() => {}, 1_000);
} else {
  setTimeout(() => {
    const server = createServer((req, res) => {
      if (req.method !== "PUT" || req.url !== "/json/new?about:blank") {
        res.writeHead(404).end();
        return;
      }
      res.end(JSON.stringify({ webSocketDebuggerUrl: `ws://127.0.0.1:${server.address().port}/devtools/page/fake` }));
    });
    server.listen(Number(portArg), "127.0.0.1", () => {
      writeFileSync(join(profile, "DevToolsActivePort"), `${server.address().port}\n/devtools/browser/fake\n`);
    });
  }, mode.delay ?? 0);
}
