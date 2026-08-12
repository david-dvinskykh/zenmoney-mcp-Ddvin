const API_BASE = "https://api.zenmoney.ru";

export interface DiffRequest {
  currentClientTimestamp: number;
  serverTimestamp: number;
  forceFetch?: string[];
  instrument?: any[];
  company?: any[];
  user?: any[];
  account?: any[];
  tag?: any[];
  merchant?: any[];
  budget?: any[];
  reminder?: any[];
  reminderMarker?: any[];
  transaction?: any[];
  deletion?: Deletion[];
}

/**
 * An object the client asks the server to delete (or the server reports as
 * deleted). `object` is the entity name used by the diff protocol —
 * "transaction", "account", "tag", "merchant", …
 */
export interface Deletion {
  id: string;
  object: string;
  stamp: number;
  user: number;
}

export interface DiffResponse {
  serverTimestamp: number;
  instrument: Instrument[];
  company: any[];
  user: User[];
  account: Account[];
  tag: Tag[];
  merchant: Merchant[];
  budget: any[];
  reminder: Reminder[];
  reminderMarker: ReminderMarker[];
  transaction: Transaction[];
  deletion: Deletion[];
}

export interface Instrument {
  id: number;
  changed: number;
  title: string;
  shortTitle: string;
  symbol: string;
  rate: number;
}

export interface User {
  id: number;
  changed: number;
  login: string;
  currency: number;
  parent: number | null;
  country: number;
  countryCode: string;
  email: string;
}

export interface Account {
  id: string;
  changed: number;
  user: number;
  instrument: number | null;
  company: number | null;
  type: string;
  title: string;
  syncID: string[] | null;
  balance: number | null;
  startBalance: number | null;
  creditLimit: number | null;
  inBalance: boolean;
  savings: boolean | null;
  enableCorrection: boolean;
  enableSMS: boolean;
  archive: boolean;
  private: boolean;
}

export interface Tag {
  id: string;
  changed: number;
  user: number;
  title: string;
  parent: string | null;
  icon: string | null;
  picture: string | null;
  color: number | null;
  showIncome: boolean;
  showOutcome: boolean;
  budgetIncome: boolean;
  budgetOutcome: boolean;
  required: boolean | null;
}

export interface Merchant {
  id: string;
  changed: number;
  user: number;
  title: string;
}

export interface Company {
  id: number;
  changed: number;
  title: string;
  country: number | null;
  fullTitle: string | null;
  www: string | null;
}

export interface Transaction {
  id: string;
  changed: number;
  created: number;
  user: number;
  deleted: boolean;
  hold: boolean | null;
  viewed: boolean;
  incomeInstrument: number;
  incomeAccount: string;
  income: number;
  incomeBankID: string | null;
  outcomeInstrument: number;
  outcomeAccount: string;
  outcome: number;
  outcomeBankID: string | null;
  opIncome: number | null;
  opIncomeInstrument: number | null;
  opOutcome: number | null;
  opOutcomeInstrument: number | null;
  tag: string[] | null;
  merchant: string | null;
  payee: string | null;
  originalPayee: string | null;
  comment: string | null;
  date: string;
  mcc: number | null;
  latitude: number | null;
  longitude: number | null;
  reminderMarker: string | null;
  qrCode: string | null;
}

/**
 * A planned transaction: either a one-off entry dated in the future or the
 * template of a repeating series. ZenMoney expands it into `ReminderMarker`
 * occurrences, so the reminder itself carries the schedule, not the dates.
 */
export interface Reminder {
  id: string;
  changed: number;
  user: number;
  incomeInstrument: number;
  incomeAccount: string;
  income: number;
  outcomeInstrument: number;
  outcomeAccount: string;
  outcome: number;
  tag: string[] | null;
  merchant: string | null;
  payee: string | null;
  comment: string | null;
  /** "day" | "week" | "month" | "year", or null for a one-off reminder. */
  interval: string | null;
  /** How many intervals between repeats: 2 with "week" means fortnightly. */
  step: number | null;
  /**
   * Positions inside the interval the series fires on. The encoding differs
   * per interval and is not documented stably, so nothing here interprets it —
   * concrete dates come from the markers instead.
   */
  points: number[] | null;
  startDate: string;
  endDate: string | null;
  notify: boolean;
}

/** One occurrence of a reminder on a specific date. */
export interface ReminderMarker {
  id: string;
  changed: number;
  user: number;
  incomeInstrument: number;
  incomeAccount: string;
  income: number;
  outcomeInstrument: number;
  outcomeAccount: string;
  outcome: number;
  tag: string[] | null;
  merchant: string | null;
  payee: string | null;
  comment: string | null;
  date: string;
  /** Id of the reminder this occurrence belongs to. */
  reminder: string;
  /** "planned" until it is turned into a transaction or dismissed. */
  state: string;
  notify: boolean;
  isForecast?: boolean;
}

export interface SuggestRequest {
  payee?: string;
  merchant?: string;
}

export interface SuggestResponse {
  tag?: string[] | null;
  merchant?: string | null;
  payee?: string | null;
}

export class ZenMoneyAPI {
  private token: string;

  constructor(token: string) {
    this.token = token;
  }

  private async request<T>(path: string, body: unknown): Promise<T> {
    const response = await fetch(`${API_BASE}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.token}`,
      },
      body: JSON.stringify(body),
    });

    if (!response.ok) {
      const text = await response.text();
      throw new Error(`ZenMoney API error ${response.status}: ${text}`);
    }

    return response.json() as Promise<T>;
  }

  async diff(req: DiffRequest): Promise<DiffResponse> {
    return this.request<DiffResponse>("/v8/diff/", req);
  }

  async suggest(
    items: SuggestRequest[]
  ): Promise<SuggestResponse[]> {
    return this.request<SuggestResponse[]>("/v8/suggest/", items);
  }
}
