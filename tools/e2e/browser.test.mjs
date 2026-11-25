import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { chmodSync, copyFileSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { Browser } from "./browser.mjs";

function fixture(t, modes) {
  const outDir = mkdtempSync(join(tmpdir(), "e2e-browser-test-"));
  const binary = join(outDir, "browser.mjs");
  const trace = join(outDir, "attempts.jsonl");
  const launches = () => readFileSync(trace, "utf8").trimEnd().split("\n").map(JSON.parse);
  copyFileSync(new URL("./fixtures/browser.mjs", import.meta.url), binary);
  chmodSync(binary, 0o700);
  writeFileSync(join(outDir, "modes.json"), JSON.stringify(modes));
  t.after(() => {
    if (existsSync(trace)) {
      for (const { profile } of launches()) rmSync(profile, { recursive: true, force: true });
    }
    rmSync(outDir, { recursive: true, force: true });
  });
  return { outDir, binary, launches };
}

test("retries an exited browser with a fresh profile and ephemeral port", async (t) => {
  const fake = fixture(t, ["exit", {}]);
  const browser = new Browser(fake);
  try {
    assert.match((await browser.start()).webSocketDebuggerUrl, /\/devtools\/page\/fake$/);
    const launches = fake.launches();
    assert.equal(launches.length, 2);
    assert.notEqual(launches[0].profile, launches[1].profile);
    assert.ok(launches[1].previousProfilesGone);
    assert.deepEqual(launches.map(({ portArg }) => portArg), ["0", "0"]);
  } finally {
    await browser.close();
  }
});

test("waits longer than one second for a slow browser", async (t) => {
  const fake = fixture(t, [{ delay: 1_300 }]);
  const browser = new Browser(fake);
  try {
    assert.match((await browser.start()).webSocketDebuggerUrl, /\/devtools\/page\/fake$/);
    assert.equal(fake.launches().length, 1);
  } finally {
    await browser.close();
  }
});

test("kills timed-out attempts and removes their profiles before retrying", async (t) => {
  const fake = fixture(t, ["hang", {}, {}]);
  const browser = new Browser({ ...fake, timeoutMs: 1_500 });
  try {
    await browser.start();
    const [failed, ready] = fake.launches();
    assert.equal(fake.launches().length, 2);
    assert.throws(() => process.kill(failed.pid, 0), { code: "ESRCH" });
    assert.ok(ready.previousProfilesGone, "timed-out attempt's profile was still present at retry");
  } finally {
    await browser.close();
  }
});

test("exhausted attempts exit 1 and publish stderr files and their tails", async (t) => {
  const fake = fixture(t, ["exit", "exit", "exit"]);
  const app = createServer((_req, res) => res.end("ok"));
  await new Promise((resolve, reject) => {
    app.once("error", reject);
    app.listen(0, "127.0.0.1", resolve);
  });
  t.after(() => app.close());
  const url = `http://127.0.0.1:${app.address().port}`;
  await assert.rejects(promisify(execFile)(process.execPath, [fileURLToPath(new URL("./firstrun.mjs", import.meta.url))], {
    env: { ...process.env, E2E_CHROME: fake.binary, E2E_OUT: fake.outDir, E2E_BASE_URL: url, E2E_MAILPIT_URL: url },
    timeout: 10_000,
  }), (err) => {
    assert.equal(err.code, 1);
    assert.match(err.stdout, /FAIL run completed \(browser failed to start after 3 attempts/);
    assert.match(err.stderr, /browser exited during start \(status 9\)/);
    const launches = fake.launches();
    assert.equal(launches.length, 3);
    assert.equal(new Set(launches.map(({ profile }) => profile)).size, 3);
    for (const [i, { profile }] of launches.entries()) {
      assert.equal(existsSync(profile), false, "failed attempt's profile was not cleaned up");
      const log = readFileSync(join(fake.outDir, `browser-stderr-${i + 1}.log`), "utf8");
      assert.ok(log.includes(`diagnostic line 1 from attempt ${i + 1}\n`));
      assert.ok(err.stderr.includes(`diagnostic line 20 from attempt ${i + 1}\n`));
    }
    assert.doesNotMatch(err.stderr, /diagnostic line 1 from attempt/);
    return true;
  });
});
