import {
  ZenMoneyAPI,
  type Account,
  type Tag,
  type Merchant,
  type Company,
  type Instrument,
  type Transaction,
  type User,
  type DiffResponse,
} from "./api.js";
import type { StateCache } from "./cache.js";

export class ZenState {
  private api: ZenMoneyAPI;
  private cache: StateCache | null;
  serverTimestamp = 0;
  accounts: Account[] = [];
  tags: Tag[] = [];
  merchants: Merchant[] = [];
  companies: Company[] = [];
  instruments: Instrument[] = [];
  transactions: Transaction[] = [];
  users: User[] = [];
  /** Unix seconds of the snapshot currently in memory (0 if never synced). */
  syncedAt = 0;
  /** Set when the data came from cache because a live sync failed. */
  staleReason: string | null = null;
  private synced = false;
  private restoredFromCache = false;
  private pendingSync: Promise<void> | null = null;

  constructor(api: ZenMoneyAPI, cache: StateCache | null = null) {
    this.api = api;
    this.cache = cache;
  }

  get isSynced(): boolean {
    return this.synced;
  }

  /** True when the in-memory data was seeded from the on-disk snapshot. */
  get isFromCache(): boolean {
    return this.restoredFromCache;
  }

  get cachePath(): string | null {
    return this.cache?.path ?? null;
  }

  /**
   * Make sure data is available before serving a tool call. Restores the
   * on-disk snapshot when there is one, then brings it up to date with an
   * incremental sync. Concurrent callers share a single in-flight attempt.
   */
  async ensureSynced(): Promise<void> {
    if (this.synced) return;
    if (!this.pendingSync) {
      this.pendingSync = this.initialSync().finally(() => {
        this.pendingSync = null;
      });
    }
    return this.pendingSync;
  }

  private async initialSync(): Promise<void> {
    const restored = await this.restoreFromCache();

    if (restored && this.isCacheFresh()) {
      this.synced = true;
      return;
    }

    try {
      await this.sync();
    } catch (error) {
      if (!restored) throw error;
      // Serve the cached snapshot rather than failing outright — the data is
      // usable, just possibly behind.
      this.synced = true;
      this.staleReason =
        error instanceof Error ? error.message : String(error);
    }
  }

  /** Seed state from the on-disk snapshot. Returns false on a cache miss. */
  async restoreFromCache(): Promise<boolean> {
    if (!this.cache) return false;

    const data = await this.cache.load();
    if (!data) return false;

    this.serverTimestamp = data.serverTimestamp;
    this.accounts = data.accounts ?? [];
    this.tags = data.tags ?? [];
    this.merchants = data.merchants ?? [];
    this.companies = data.companies ?? [];
    this.instruments = data.instruments ?? [];
    // A snapshot should never hold deleted transactions, but drop any that a
    // previous version (or a partial write) left behind rather than serving them.
    this.transactions = (data.transactions ?? []).filter((t) => !t.deleted);
    this.users = data.users ?? [];
    this.syncedAt = data.savedAt;
    this.restoredFromCache = true;
    return true;
  }

  /**
   * Skip the network entirely while the snapshot is younger than
   * ZENMONEY_CACHE_TTL seconds. Defaults to 0 — always revalidate, which is
   * cheap because the sync is incremental from the cached timestamp.
   */
  private isCacheFresh(): boolean {
    const ttl = Number(process.env.ZENMONEY_CACHE_TTL ?? 0);
    if (!Number.isFinite(ttl) || ttl <= 0) return false;
    return Math.floor(Date.now() / 1000) - this.syncedAt < ttl;
  }

  async sync(forceFull = false): Promise<DiffResponse> {
    if (forceFull) {
      // Drop everything so a full re-download cannot leave stale entities
      // behind (the merge is additive and would otherwise keep them).
      this.accounts = [];
      this.tags = [];
      this.merchants = [];
      this.companies = [];
      this.instruments = [];
      this.transactions = [];
      this.users = [];
      this.restoredFromCache = false;
    } else if (!this.synced && !this.restoredFromCache) {
      // Fresh process: pick up where the last one left off so this sync is
      // incremental instead of a full re-download.
      await this.restoreFromCache();
    }

    const timestamp = forceFull ? 0 : this.serverTimestamp;

    const req: any = {
      currentClientTimestamp: Math.floor(Date.now() / 1000),
      serverTimestamp: timestamp,
    };

    if (timestamp === 0) {
      req.forceFetch = [
        "instrument",
        "company",
        "account",
        "tag",
        "merchant",
        "transaction",
        "user",
      ];
    }

    const resp = await this.api.diff(req);
    this.applyDiff(resp);
    this.synced = true;
    this.syncedAt = Math.floor(Date.now() / 1000);
    this.staleReason = null;
    await this.persist();
    return resp;
  }

  /**
   * Record a transaction that was just pushed to ZenMoney so the local
   * snapshot (memory and disk) stays consistent without another round trip.
   */
  async applyLocalTransaction(
    transaction: Transaction,
    serverTimestamp: number
  ): Promise<void> {
    this.serverTimestamp = serverTimestamp;
    this.transactions.push(transaction);
    await this.persist();
  }

  /** Drop the on-disk snapshot (used by force_full re-downloads). */
  async clearCache(): Promise<void> {
    if (!this.cache) return;
    try {
      await this.cache.clear();
    } catch {
      // Best effort — the next successful sync overwrites it anyway.
    }
  }

  /** Write the current snapshot to disk. Never throws — caching is best effort. */
  async persist(): Promise<void> {
    if (!this.cache) return;
    try {
      await this.cache.save({
        serverTimestamp: this.serverTimestamp,
        accounts: this.accounts,
        tags: this.tags,
        merchants: this.merchants,
        companies: this.companies,
        instruments: this.instruments,
        transactions: this.transactions,
        users: this.users,
      });
    } catch (error) {
      console.error(
        `Failed to write ZenMoney cache: ${error instanceof Error ? error.message : String(error)}`
      );
    }
  }

  private applyDiff(resp: DiffResponse): void {
    this.serverTimestamp = resp.serverTimestamp;
    this.mergeEntities("instruments", resp.instrument, (e) => e.id);
    this.mergeEntities("accounts", resp.account, (e) => e.id);
    this.mergeEntities("tags", resp.tag, (e) => e.id);
    this.mergeEntities("merchants", resp.merchant, (e) => e.id);
    this.mergeEntities("companies", resp.company, (e) => e.id);
    this.mergeEntities("users", resp.user, (e) => e.id);
    this.mergeTransactions(resp.transaction);

    if (resp.deletion) {
      for (const del of resp.deletion) {
        this.applyDeletion(del.object, del.id);
      }
    }
  }

  private mergeEntities<T>(
    field: keyof this,
    incoming: T[],
    getId: (e: T) => string | number
  ): void {
    if (!incoming || incoming.length === 0) return;

    const arr = this[field] as T[];
    const map = new Map<string | number, T>();
    for (const item of arr) {
      map.set(getId(item), item);
    }
    for (const item of incoming) {
      map.set(getId(item), item);
    }
    (this[field] as T[]) = Array.from(map.values());
  }

  private mergeTransactions(incoming: Transaction[]): void {
    if (!incoming || incoming.length === 0) return;

    const map = new Map<string, Transaction>();
    for (const t of this.transactions) {
      map.set(t.id, t);
    }
    for (const t of incoming) {
      if (t.deleted) {
        map.delete(t.id);
      } else {
        map.set(t.id, t);
      }
    }
    this.transactions = Array.from(map.values());
  }

  private applyDeletion(objectType: string, id: string): void {
    switch (objectType) {
      case "transaction":
        this.transactions = this.transactions.filter((t) => t.id !== id);
        break;
      case "account":
        this.accounts = this.accounts.filter((a) => a.id !== id);
        break;
      case "tag":
        this.tags = this.tags.filter((t) => t.id !== id);
        break;
      case "merchant":
        this.merchants = this.merchants.filter((m) => m.id !== id);
        break;
    }
  }

  getActiveAccounts(): Account[] {
    return this.accounts.filter((a) => !a.archive);
  }

  getInstrument(id: number): Instrument | undefined {
    return this.instruments.find((i) => i.id === id);
  }

  getCompany(id: number): Company | undefined {
    return this.companies.find((c) => c.id === id);
  }

  getUser(): User | undefined {
    return this.users.find((u) => u.parent === null) ?? this.users[0];
  }

  findAccountByName(name: string): Account | undefined {
    const lower = name.toLowerCase();
    return this.accounts.find(
      (a) => a.title.toLowerCase().includes(lower)
    );
  }

  findTagByName(name: string): Tag | undefined {
    const lower = name.toLowerCase();
    return this.tags.find(
      (t) => t.title.toLowerCase().includes(lower)
    );
  }

  findMerchantByName(name: string): Merchant | undefined {
    const lower = name.toLowerCase();
    return this.merchants.find(
      (m) => m.title.toLowerCase().includes(lower)
    );
  }

  getTagHierarchy(): { parent: Tag; children: Tag[] }[] {
    const parentTags = this.tags.filter((t) => !t.parent);
    return parentTags.map((parent) => ({
      parent,
      children: this.tags.filter((t) => t.parent === parent.id),
    }));
  }
}
