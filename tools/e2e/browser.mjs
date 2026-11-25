import { spawn } from "node:child_process";
import {
  accessSync, closeSync, constants, existsSync, fstatSync, mkdtempSync,
  openSync, readFileSync, readSync, rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const attempts = 3;

export function findBrowser() {
  const wanted = process.env.E2E_CHROME ? [process.env.E2E_CHROME] : ["chromium", "google-chrome", "chrome"];
  for (const name of wanted) {
    if (name.includes("/")) {
      if (existsSync(name)) return name;
      continue;
    }
    for (const dir of (process.env.PATH ?? "").split(delimiter)) {
      const candidate = join(dir, name);
      try {
        accessSync(candidate, constants.X_OK);
        return candidate;
      } catch {}
    }
  }
  throw new Error("no browser found: set E2E_CHROME or put chromium, google-chrome or chrome on PATH");
}

function stderrTail(path) {
  const fd = openSync(path, "r");
  try {
    const size = fstatSync(fd).size;
    const buf = Buffer.alloc(Math.min(size, 8192));
    const n = readSync(fd, buf, 0, buf.length, size - buf.length);
    return buf.subarray(0, n).toString("utf8").trimEnd().split("\n").slice(-12).join("\n");
  } finally {
    closeSync(fd);
  }
}

async function waitForTarget(profile, stopped, timeoutMs) {
  const deadline = performance.now() + timeoutMs;
  const portFile = join(profile, "DevToolsActivePort");
  const expired = new Error("browser did not expose its debugging port");
  const signal = AbortSignal.any([stopped, AbortSignal.timeout(timeoutMs)]);
  let backoff = 50;
  let port;
  while (!signal.aborted && performance.now() < deadline) {
    if (!port) {
      try {
        // The second line confirms Chromium finished writing the port file.
        const [number, endpoint] = readFileSync(portFile, "utf8").split("\n");
        if (/^\d+$/.test(number) && Number(number) > 0 && Number(number) <= 65535 &&
            endpoint?.startsWith("/devtools/browser/")) port = Number(number);
      } catch (err) {
        if (err.code !== "ENOENT") throw err;
      }
    }
    if (port) {
      try {
        const res = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, {
          method: "PUT",
          signal: AbortSignal.any([signal, AbortSignal.timeout(1_000)]),
        });
        if (res.ok) {
          const target = await res.json();
          if (target.webSocketDebuggerUrl && !signal.aborted) return target;
        } else {
          await res.body?.cancel();
        }
      } catch {}
    }
    try {
      await delay(backoff, undefined, { signal });
    } catch (err) {
      if (!signal.aborted) throw err;
    }
    backoff = Math.min(backoff * 2, 1_000);
  }
  throw stopped.aborted ? stopped.reason : expired;
}

export class Browser {
  #attempt;

  constructor({ binary, outDir, noSandbox = false, timeoutMs = 30_000 }) {
    this.binary = resolve(binary);
    this.outDir = outDir;
    this.noSandbox = noSandbox;
    this.timeoutMs = timeoutMs;
  }

  async start() {
    const failures = [];
    for (let n = 1; n <= attempts; n++) {
      const log = join(this.outDir, `browser-stderr-${n}.log`);
      try {
        return await this.#launch(log);
      } catch (err) {
        await this.close();
        failures.push({ log, reason: err.message });
      }
    }
    for (const { log, reason } of failures) {
      let tail;
      try { tail = stderrTail(log); } catch (err) { tail = `cannot read stderr: ${err.message}`; }
      console.error(`${log} (${reason}):\n${tail || "(no stderr)"}`);
    }
    throw new Error(`browser failed to start after ${attempts} attempts: ${failures.map(({ reason }) => reason).join("; ")}`);
  }

  async #launch(log) {
    const profile = mkdtempSync(resolve(tmpdir(), "e2e-profile-"));
    const attempt = this.#attempt = { profile };
    // Let Chromium bind an ephemeral port itself. Reserving and releasing a
    // port before launch lets another process claim it in the meantime.
    const args = [
      "--headless=new", "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1",
      `--user-data-dir=${profile}`, "--no-first-run", "--no-default-browser-check",
      "--disable-gpu", "--disable-extensions", "--disable-background-networking", "about:blank",
    ];
    if (this.noSandbox) args.unshift("--no-sandbox");
    const fd = openSync(log, "w", 0o600);
    try {
      // Chromium creates a singleton socket outside --user-data-dir unless its
      // temp directory is scoped too. A short relative socket path avoids the
      // Unix socket path limit when the caller's TMPDIR is long.
      attempt.child = spawn(this.binary, args, {
        cwd: profile, env: { ...process.env, TMPDIR: "." },
        stdio: ["ignore", "ignore", fd], detached: true,
      });
    } finally {
      closeSync(fd);
    }
    const stopped = new AbortController();
    attempt.child.once("error", (err) => stopped.abort(err));
    attempt.child.once("exit", (code, signal) => {
      stopped.abort(new Error(`browser exited during start (${signal ?? `status ${code}`})`));
    });
    attempt.closed = new Promise((resolve) => attempt.child.once("close", resolve));
    return waitForTarget(profile, stopped.signal, this.timeoutMs);
  }

  #kill() {
    const pid = this.#attempt?.child?.pid;
    if (pid) {
      // The detached child owns this process group, including any renderers.
      try { process.kill(-pid, "SIGKILL"); } catch (err) {
        if (err.code !== "ESRCH") throw err;
      }
    }
  }

  #removeProfile(attempt) {
    if (this.#attempt === attempt) {
      rmSync(attempt.profile, { recursive: true, force: true });
      this.#attempt = undefined;
    }
  }

  async close() {
    const attempt = this.#attempt;
    if (!attempt) return;
    this.#kill();
    await attempt.closed;
    this.#removeProfile(attempt);
  }

  // Exit handlers cannot wait for the child, so make a best effort in place.
  closeOnExit() {
    if (!this.#attempt) return;
    this.#kill();
    this.#removeProfile(this.#attempt);
  }
}
