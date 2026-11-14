import { expect, test, vi } from "vitest";
import { prepareEnterpriseProvider } from "../src/provisioning/index.js";
import { HeimdallError } from "../src/internal/errors.js";

test("provider creation requires a dispatch claim and recovery only reads", async () => {
  const signal = AbortSignal.timeout(5000);
  const options = {
    issuer: "https://login.example",
    organizationId: "org-a",
    name: "Directory",
    token: "operator-token",
    provider: {
      protocol: "oidc" as const,
      issuer: "https://directory.example",
      clientId: "client",
    },
  };
  let dispatched = false;
  let owner = "org-a";
  const fetch = vi.fn<(url: string, init: RequestInit) => Promise<Response>>(async (_url, init) => {
    if (init.method === "POST") {
      if (!dispatched) return Response.json({ id: "unexpected-write" });
      throw new Error("successful response was lost");
    }
    return Response.json({
      idp: {
        id: "provider-a",
        name: "Directory",
        state: "IDP_STATE_ACTIVE",
        type: "IDP_TYPE_OIDC",
        details: { resourceOwner: owner },
        config: {
          options: {},
          oidc: {
            issuer: "https://directory.example",
            clientId: "client",
            scopes: ["email", "profile", "openid"],
          },
        },
      },
    });
  });
  vi.stubGlobal("fetch", fetch);
  try {
    const create = { ...options, provider: { ...options.provider, clientSecret: "secret" } };
    await expect(
      prepareEnterpriseProvider(
        {
          ...create,
          claimDispatch: async () => {
            throw new HeimdallError("conflict");
          },
        },
        signal,
      ),
    ).rejects.toMatchObject({ code: "conflict" });
    expect(fetch).not.toHaveBeenCalled();
    await expect(
      prepareEnterpriseProvider(
        {
          ...create,
          claimDispatch: async () => {
            dispatched = true;
          },
        },
        signal,
      ),
    ).rejects.toMatchObject({ code: "outcome_unknown" });
    const recovery = { ...options, existingProviderId: "provider-a" };
    await expect(prepareEnterpriseProvider(recovery, signal)).resolves.toEqual({
      organizationId: "org-a",
      providerId: "provider-a",
      protocol: "oidc",
      callbackUrl: "https://login.example/idps/callback",
    });
    owner = "org-b";
    await expect(prepareEnterpriseProvider(recovery, signal)).rejects.toMatchObject({
      code: "conflict",
    });
    expect(fetch.mock.calls.map(([, init]) => init.method)).toEqual(["POST", "GET", "GET"]);
    expect(fetch.mock.calls.map(([url]) => url)).toEqual([
      "https://login.example/management/v1/idps/generic_oidc",
      "https://login.example/v2/idps/provider-a",
      "https://login.example/v2/idps/provider-a",
    ]);
    expect(
      fetch.mock.calls.map(([, init]) => new Headers(init.headers).get("x-zitadel-orgid")),
    ).toEqual(["org-a", "org-a", "org-a"]);
    expect(JSON.parse(fetch.mock.calls[0]![1].body as string)).toMatchObject({
      usePkce: true,
      providerOptions: {
        isLinkingAllowed: false,
        isCreationAllowed: false,
        isAutoCreation: false,
        isAutoUpdate: false,
      },
    });
  } finally {
    vi.unstubAllGlobals();
  }
});
