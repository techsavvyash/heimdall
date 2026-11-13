import { HeimdallError } from "./errors.js";

export class ProviderThrottled extends HeimdallError {
  readonly retryDelay: number;
  constructor(header: string | null) {
    super("unavailable");
    const delay =
      header && /^[0-9]+$/.test(header)
        ? Number(header) * 1000
        : header
          ? Date.parse(header) - Date.now()
          : 0;
    this.retryDelay = Number.isSafeInteger(delay) && delay > 0 ? Math.min(delay, 86400000) : 0;
  }
}
