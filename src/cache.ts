import { createHash } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import type {
  Account,
  Company,
  Instrument,
  Merchant,
  Tag,
  Transaction,
  User,
} from "./api.js";

export const CACHE_VERSION = 1;

export interface CacheData {
  version: number;
  /** Unix seconds when this snapshot was written. */
  savedAt: number;
  serverTimestamp: number;
  accounts: Account[];
  tags: Tag[];
  merchants: Merchant[];
  companies: Company[];
  instruments: Instrument[];
  transactions: Transaction[];
  users: User[];
}

/**
 * Stable, non-reversible id for a token. Used as the cache file name so
 * separate accounts never share a snapshot and the token itself is never
 * written to disk.
 */
export function tokenHash(token: string): string {
  return createHash("sha256").update(token).digest("hex").slice(0, 32);
}

export function cacheBaseDir(): string {
  const override = process.env.ZENMONEY_CACHE_DIR;
  if (override) return override;
  if (process.platform === "win32" && process.env.LOCALAPPDATA) {
    return join(process.env.LOCALAPPDATA, "zenmoney-mcp");
  }
  if (process.env.XDG_CACHE_HOME) {
    return join(process.env.XDG_CACHE_HOME, "zenmoney-mcp");
  }
  return join(homedir(), ".cache", "zenmoney-mcp");
}

/**
 * File-backed snapshot of the synced state, keyed by the hash of the API
 * token. Lets short-lived stdio processes reuse a previous full sync instead
 * of re-downloading everything on every launch.
 */
export class StateCache {
  readonly path: string;
  private writeChain: Promise<void> = Promise.resolve();

  constructor(token: string, dir: string = cacheBaseDir()) {
    this.path = join(dir, `${tokenHash(token)}.json`);
  }

  async load(): Promise<CacheData | null> {
    let raw: string;
    try {
      raw = await readFile(this.path, "utf8");
    } catch {
      return null;
    }

    try {
      const parsed = JSON.parse(raw) as CacheData;
      if (parsed?.version !== CACHE_VERSION) return null;
      if (typeof parsed.serverTimestamp !== "number") return null;
      return parsed;
    } catch {
      // Corrupted snapshot — treat it as a cache miss and let a full sync
      // overwrite it.
      return null;
    }
  }

  /**
   * Atomically replace the snapshot. Writes are chained so concurrent callers
   * cannot interleave into a half-written file.
   */
  async save(data: Omit<CacheData, "version" | "savedAt">): Promise<void> {
    const payload: CacheData = {
      ...data,
      version: CACHE_VERSION,
      savedAt: Math.floor(Date.now() / 1000),
    };

    this.writeChain = this.writeChain.then(() => this.writeSnapshot(payload));
    return this.writeChain;
  }

  private async writeSnapshot(payload: CacheData): Promise<void> {
    const dir = dirname(this.path);
    const tmp = `${this.path}.${process.pid}.tmp`;
    await mkdir(dir, { recursive: true, mode: 0o700 });
    await writeFile(tmp, JSON.stringify(payload), { mode: 0o600 });
    await rename(tmp, this.path);
  }

  async clear(): Promise<void> {
    await rm(this.path, { force: true });
  }
}
