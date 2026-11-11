import { randomUUID } from "node:crypto";
import { IdentityVerifier } from "./identity-verifier.js";
import { bounded, commitAuditEvent, known } from "./audited-decision.js";
import { checkCancellation, HeimdallError } from "./errors.js";

type Arguments = ConstructorParameters<typeof IdentityVerifier>;
type Principal = Awaited<ReturnType<IdentityVerifier["verify"]>>;
// Internal composition. Identity metadata comes only from this captured verifier.
export class AuditedAuthentication {
  private readonly verifier: IdentityVerifier;
  private readonly productId: string;
  private readonly environment: string;
  private readonly policyVersion: string;
  private readonly releaseWindow: number;
  constructor(
    config: Arguments[0],
    secret: Arguments[1],
    gate: Arguments[2],
    private readonly append: Parameters<typeof commitAuditEvent>[1],
    private readonly validate: Parameters<typeof commitAuditEvent>[2],
    private readonly now: () => number = Date.now,
  ) {
    if (
      ![config.productId, config.environment].every((v) => /^[A-Za-z0-9_-]{1,64}$/.test(v)) ||
      typeof append !== "function" ||
      typeof validate !== "function"
    )
      throw new HeimdallError("invalid_argument");
    this.verifier = new IdentityVerifier(config, secret, gate, now);
    this.productId = config.productId;
    this.environment = config.environment;
    this.policyVersion = config.policyVersion;
    this.releaseWindow = config.freshnessMs === 0 ? 5000 : (config.freshnessMs ?? 60000);
  }
  owns(value: unknown): boolean {
    return this.verifier.owns(value);
  }
  async verify(raw: string, context: string, caller?: AbortSignal): Promise<Principal> {
    const operation = AbortSignal.any([AbortSignal.timeout(5000), ...(caller ? [caller] : [])]);
    let principal: Principal | undefined;
    let failure: HeimdallError | undefined;
    try {
      principal = await bounded((signal) => this.verifier.verify(raw, context, signal), operation);
      checkCancellation(operation);
    } catch (error) {
      failure = known(error);
    }
    const outcome = failure
      ? failure.code === "cancelled"
        ? "cancelled"
        : failure.code === "invalid_argument"
          ? "invalid"
          : ["unauthenticated", "forbidden"].includes(failure.code)
            ? "deny"
            : "unavailable"
      : "succeeded";
    const recordedAt = this.now();
    const event = {
      schemaVersion: 1,
      eventId: randomUUID(),
      eventType: "heimdall.authentication.completed",
      eventVersion: 1,
      occurredAt: recordedAt,
      recordedAt,
      productId: this.productId,
      environment: this.environment,
      tenantId: null,
      actor: principal
        ? { kind: "user", issuer: principal.issuer, subject: principal.subject }
        : { kind: "unknown" },
      target: { type: "product", id: this.productId },
      action: "authenticate",
      outcome,
      requestId: randomUUID(),
      source: "sdk",
      sdkVersion: "0.0.0",
      policyVersion: this.policyVersion,
      attributes: {},
      ...(failure ? { reasonCode: failure.code } : {}),
    };
    await commitAuditEvent(event, this.append, this.validate);
    if (failure) throw failure;
    try {
      checkCancellation(operation);
      const releasedAt = this.now();
      if (releasedAt >= principal!.expiresAt) throw new HeimdallError("unauthenticated");
      if (
        releasedAt < principal!.identityValidatedAt ||
        releasedAt - principal!.identityValidatedAt >= this.releaseWindow
      )
        throw new HeimdallError("unavailable");
    } catch (error) {
      const rejected = known(error);
      const at = Math.max(event.recordedAt, this.now());
      await commitAuditEvent(
        {
          ...event,
          eventId: randomUUID(),
          occurredAt: at,
          recordedAt: at,
          outcome:
            rejected.code === "cancelled"
              ? "cancelled"
              : rejected.code === "unauthenticated"
                ? "deny"
                : "unavailable",
          reasonCode: rejected.code,
        },
        this.append,
        this.validate,
      );
      throw rejected;
    }
    return principal!;
  }
}
