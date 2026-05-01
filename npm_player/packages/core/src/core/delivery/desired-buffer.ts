export type DesiredBufferValue = number | (() => number);

export interface DesiredBufferModelOptions {
  baseMs?: DesiredBufferValue;
  minMs?: number;
  maxMs?: number;
  keepAwayPenaltyStepMs?: number;
  keepAwayRelaxStepMs?: number;
  maxKeepAwayMs?: number;
}

export class DesiredBufferModel {
  private readonly factors = new Map<string, DesiredBufferValue>();
  private keepAwayExtraMs = 0;
  private readonly options: Required<DesiredBufferModelOptions>;

  constructor(options: DesiredBufferModelOptions = {}) {
    this.options = {
      baseMs: options.baseMs ?? 0,
      minMs: options.minMs ?? 0,
      maxMs: options.maxMs ?? Infinity,
      keepAwayPenaltyStepMs: options.keepAwayPenaltyStepMs ?? 100,
      keepAwayRelaxStepMs: options.keepAwayRelaxStepMs ?? 50,
      maxKeepAwayMs: options.maxKeepAwayMs ?? 500,
    };
  }

  setFactor(name: string, value: DesiredBufferValue): void {
    this.factors.set(name, value);
  }

  removeFactor(name: string): void {
    this.factors.delete(name);
  }

  getFactor(name: string): DesiredBufferValue | undefined {
    return this.factors.get(name);
  }

  getKeepAwayExtraMs(): number {
    return this.keepAwayExtraMs;
  }

  penalize(amountMs = this.options.keepAwayPenaltyStepMs): number {
    this.keepAwayExtraMs = Math.min(this.keepAwayExtraMs + amountMs, this.options.maxKeepAwayMs);
    return this.keepAwayExtraMs;
  }

  relax(amountMs = this.options.keepAwayRelaxStepMs): number {
    this.keepAwayExtraMs = Math.max(0, this.keepAwayExtraMs - amountMs);
    return this.keepAwayExtraMs;
  }

  reset(): void {
    this.keepAwayExtraMs = 0;
  }

  getDesiredMs(): number {
    let desired = this.resolve(this.options.baseMs) + this.keepAwayExtraMs;
    for (const value of this.factors.values()) {
      desired += this.resolve(value);
    }
    return Math.min(Math.max(Math.round(desired), this.options.minMs), this.options.maxMs);
  }

  private resolve(value: DesiredBufferValue): number {
    const resolved = typeof value === "function" ? value() : value;
    return Number.isFinite(resolved) ? resolved : 0;
  }
}
