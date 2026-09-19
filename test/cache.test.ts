import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { ZenMoneyAPI } from "../src/api.js";
import { CACHE_VERSION, StateCache, cacheBaseDir, tokenHash } from "../src/cache.js";
import { ZenState } from "../src/state.js";
import { makeDiffResponse, makeTransaction } from "./fixtures.js";

let dir: string;

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), "zenmoney-cache-"));
});

afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
  delete process.env.ZENMONEY_CACHE_TTL;
  delete process.env.ZENMONEY_CACHE_DIR;
});

function createApi(resp = makeDiffResponse()) {
  return {
    diff: vi.fn().mockResolvedValue(resp),
    suggest: vi.fn(),
  } as unknown as ZenMoneyAPI;
}

describe("tokenHash", () => {
  it("should be stable for the same token", () => {
    expect(tokenHash("abc")).toBe(tokenHash("abc"));
  });

  it("should differ between tokens", () => {
    expect(tokenHash("abc")).not.toBe(tokenHash("abd"));
  });

  it("should not leak the token itself", () => {
    const hash = tokenHash("super-secret-token");
    expect(hash).not.toContain("super-secret-token");
    expect(hash).toMatch(/^[0-9a-f]{32}$/);
  });
});

describe("cacheBaseDir", () => {
  it("should honour ZENMONEY_CACHE_DIR", () => {
    process.env.ZENMONEY_CACHE_DIR = "/custom/place";
    expect(cacheBaseDir()).toBe("/custom/place");
  });
});

describe("StateCache", () => {
  it("should name the file after the token hash", () => {
    const cache = new StateCache("my-token", dir);
    expect(cache.path).toBe(join(dir, `${tokenHash("my-token")}.json`));
  });

  it("should give different tokens different files", () => {
    expect(new StateCache("token-a", dir).path).not.toBe(
      new StateCache("token-b", dir).path
    );
  });

  it("should return null when there is no snapshot", async () => {
    expect(await new StateCache("t", dir).load()).toBeNull();
  });

  it("should round-trip a snapshot", async () => {
    const cache = new StateCache("t", dir);
    const tx = makeTransaction({ id: "tx-1" });
    await cache.save({
      serverTimestamp: 1700000000,
      accounts: [],
      tags: [],
      merchants: [],
      companies: [],
      instruments: [],
      transactions: [tx],
      users: [],
      reminders: [],
      reminderMarkers: [],
    });

    const loaded = await cache.load();
    expect(loaded?.serverTimestamp).toBe(1700000000);
    expect(loaded?.transactions).toHaveLength(1);
    expect(loaded?.transactions[0].id).toBe("tx-1");
    expect(loaded?.version).toBe(CACHE_VERSION);
    expect(loaded?.savedAt).toBeGreaterThan(0);
  });

  it("should write the snapshot with owner-only permissions", async () => {
    const cache = new StateCache("t", dir);
    await cache.save(emptySnapshot());

    const mode = (await stat(cache.path)).mode & 0o777;
    expect(mode).toBe(0o600);
  });

  it("should treat a corrupted snapshot as a miss", async () => {
    const cache = new StateCache("t", dir);
    await cache.save(emptySnapshot());
    await writeFile(cache.path, "{not json");

    expect(await cache.load()).toBeNull();
  });

  it("should ignore snapshots written by a different cache version", async () => {
    const cache = new StateCache("t", dir);
    await cache.save(emptySnapshot());
    const raw = JSON.parse(await readFile(cache.path, "utf8"));
    await writeFile(cache.path, JSON.stringify({ ...raw, version: 999 }));

    expect(await cache.load()).toBeNull();
  });

  it("should not interleave concurrent writes", async () => {
    const cache = new StateCache("t", dir);
    await Promise.all([
      cache.save({ ...emptySnapshot(), serverTimestamp: 1 }),
      cache.save({ ...emptySnapshot(), serverTimestamp: 2 }),
      cache.save({ ...emptySnapshot(), serverTimestamp: 3 }),
    ]);

    expect((await cache.load())?.serverTimestamp).toBe(3);
  });

  it("should clear the snapshot", async () => {
    const cache = new StateCache("t", dir);
    await cache.save(emptySnapshot());
    await cache.clear();

    expect(existsSync(cache.path)).toBe(false);
    expect(await cache.load()).toBeNull();
  });

  it("should not throw when clearing a missing snapshot", async () => {
    await expect(new StateCache("t", dir).clear()).resolves.toBeUndefined();
  });
});

describe("ZenState persistence", () => {
  it("should write a snapshot after sync", async () => {
    const cache = new StateCache("t", dir);
    const state = new ZenState(createApi(), cache);
    await state.sync();

    const loaded = await cache.load();
    expect(loaded?.accounts).toHaveLength(4);
    expect(loaded?.tags).toHaveLength(3);
    expect(loaded?.serverTimestamp).toBe(1700000000);
  });

  it("should restore a snapshot written by an earlier process", async () => {
    const cache = new StateCache("t", dir);
    const first = new ZenState(createApi(), cache);
    await first.ensureSynced();

    // A second process, same token: same cache file.
    const secondApi = createApi(
      makeDiffResponse({
        serverTimestamp: 1700000050,
        account: [],
        tag: [],
        instrument: [],
        merchant: [],
        company: [],
        user: [],
        transaction: [],
        deletion: [],
      })
    );
    const second = new ZenState(secondApi, new StateCache("t", dir));
    await second.ensureSynced();

    expect(second.isFromCache).toBe(true);
    expect(second.accounts).toHaveLength(4);
    // Incremental: continues from the cached timestamp instead of 0.
    expect(secondApi.diff).toHaveBeenCalledWith(
      expect.objectContaining({ serverTimestamp: 1700000000 })
    );
    expect(vi.mocked(secondApi.diff).mock.calls[0][0]).not.toHaveProperty(
      "forceFetch"
    );
  });

  it("should drop deleted transactions found in a snapshot", async () => {
    const cache = new StateCache("t", dir);
    await cache.save({
      serverTimestamp: 1700000000,
      accounts: [],
      tags: [],
      merchants: [],
      companies: [],
      instruments: [],
      transactions: [
        makeTransaction({ id: "tx-live" }),
        makeTransaction({ id: "tx-gone", deleted: true }),
      ],
      users: [],
      reminders: [],
      reminderMarkers: [],
    });

    const state = new ZenState(createApi(), new StateCache("t", dir));
    await state.restoreFromCache();

    expect(state.transactions.map((t) => t.id)).toEqual(["tx-live"]);
  });

  it("should not share snapshots between tokens", async () => {
    const stateA = new ZenState(createApi(), new StateCache("token-a", dir));
    await stateA.sync();

    const stateB = new ZenState(createApi(), new StateCache("token-b", dir));
    expect(await stateB.restoreFromCache()).toBe(false);
  });

  it("should persist locally added transactions", async () => {
    const cache = new StateCache("t", dir);
    const state = new ZenState(createApi(), cache);
    await state.sync();

    const tx = makeTransaction({ id: "tx-local" });
    await state.applyLocalTransaction(
      tx,
      makeDiffResponse({ serverTimestamp: 1700000123 })
    );

    const loaded = await cache.load();
    expect(loaded?.serverTimestamp).toBe(1700000123);
    expect(loaded?.transactions.map((t) => t.id)).toContain("tx-local");
  });

  it("should survive an unwritable cache directory", async () => {
    // A regular file where the cache directory should be: mkdir fails.
    const blocker = join(dir, "blocker");
    await writeFile(blocker, "not a directory");

    const state = new ZenState(
      createApi(),
      new StateCache("t", join(blocker, "nested"))
    );
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    await expect(state.sync()).resolves.toBeDefined();
    expect(state.isSynced).toBe(true);
    errSpy.mockRestore();
  });

  it("should work without a cache at all", async () => {
    const state = new ZenState(createApi());
    await state.ensureSynced();

    expect(state.isSynced).toBe(true);
    expect(state.cachePath).toBeNull();
    expect(state.isFromCache).toBe(false);
  });
});

describe("ZenState.ensureSynced", () => {
  it("should sync on first call and stay quiet afterwards", async () => {
    const api = createApi();
    const state = new ZenState(api, new StateCache("t", dir));

    await state.ensureSynced();
    await state.ensureSynced();

    expect(api.diff).toHaveBeenCalledTimes(1);
    expect(state.isSynced).toBe(true);
  });

  it("should collapse concurrent callers into one sync", async () => {
    const api = createApi();
    const state = new ZenState(api, new StateCache("t", dir));

    await Promise.all([
      state.ensureSynced(),
      state.ensureSynced(),
      state.ensureSynced(),
    ]);

    expect(api.diff).toHaveBeenCalledTimes(1);
  });

  it("should retry after a failed attempt", async () => {
    const api = createApi();
    vi.mocked(api.diff).mockRejectedValueOnce(new Error("Network error"));
    const state = new ZenState(api, new StateCache("t", dir));

    await expect(state.ensureSynced()).rejects.toThrow("Network error");
    expect(state.isSynced).toBe(false);

    await state.ensureSynced();
    expect(state.isSynced).toBe(true);
    expect(api.diff).toHaveBeenCalledTimes(2);
  });

  it("should fall back to the cached snapshot when the sync fails", async () => {
    const cache = new StateCache("t", dir);
    await new ZenState(createApi(), cache).sync();

    const api = createApi();
    vi.mocked(api.diff).mockRejectedValue(new Error("Network error"));
    const state = new ZenState(api, new StateCache("t", dir));

    await state.ensureSynced();

    expect(state.isSynced).toBe(true);
    expect(state.accounts).toHaveLength(4);
    expect(state.staleReason).toContain("Network error");
  });

  it("should throw when the sync fails and there is no cache", async () => {
    const api = createApi();
    vi.mocked(api.diff).mockRejectedValue(new Error("Auth failed"));
    const state = new ZenState(api, new StateCache("t", dir));

    await expect(state.ensureSynced()).rejects.toThrow("Auth failed");
  });

  it("should skip the network while the snapshot is within ZENMONEY_CACHE_TTL", async () => {
    const cache = new StateCache("t", dir);
    await new ZenState(createApi(), cache).sync();

    process.env.ZENMONEY_CACHE_TTL = "3600";
    const api = createApi();
    const state = new ZenState(api, new StateCache("t", dir));
    await state.ensureSynced();

    expect(api.diff).not.toHaveBeenCalled();
    expect(state.isSynced).toBe(true);
    expect(state.accounts).toHaveLength(4);
  });

  it("should revalidate once the TTL has elapsed", async () => {
    const cache = new StateCache("t", dir);
    await new ZenState(createApi(), cache).sync();

    // TTL of 1s against a snapshot backdated well past it.
    const raw = JSON.parse(await readFile(cache.path, "utf8"));
    await writeFile(
      cache.path,
      JSON.stringify({ ...raw, savedAt: raw.savedAt - 9999 })
    );
    process.env.ZENMONEY_CACHE_TTL = "1";

    const api = createApi();
    const state = new ZenState(api, new StateCache("t", dir));
    await state.ensureSynced();

    expect(api.diff).toHaveBeenCalledTimes(1);
  });
});

describe("ZenState force full sync", () => {
  it("should drop stale entities instead of merging them", async () => {
    const cache = new StateCache("t", dir);
    const api = createApi();
    const state = new ZenState(api, cache);
    await state.sync();
    expect(state.accounts).toHaveLength(4);

    vi.mocked(api.diff).mockResolvedValue(
      makeDiffResponse({
        serverTimestamp: 1700000100,
        account: [],
        tag: [],
        instrument: [],
        merchant: [],
        company: [],
        user: [],
        transaction: [],
        deletion: [],
      })
    );

    await state.sync(true);

    expect(state.accounts).toHaveLength(0);
    expect((await cache.load())?.accounts).toHaveLength(0);
  });

  it("should request a full re-download", async () => {
    const api = createApi();
    const state = new ZenState(api, new StateCache("t", dir));
    await state.sync();
    await state.sync(true);

    expect(api.diff).toHaveBeenLastCalledWith(
      expect.objectContaining({
        serverTimestamp: 0,
        forceFetch: expect.any(Array),
      })
    );
  });
});

function emptySnapshot() {
  return {
    serverTimestamp: 0,
    accounts: [],
    tags: [],
    merchants: [],
    companies: [],
    instruments: [],
    transactions: [],
    users: [],
    reminders: [],
    reminderMarkers: [],
  };
}
