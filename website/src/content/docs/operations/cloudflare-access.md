---
title: Cloudflare Access migration
---

Cloudflare Access is an optional authentication profile for a deployment that
needs Access-protected MCP or browser Viz traffic. Native `/memory` API-key
authentication and optional generic OAuth remain available independently.
There is no change to the stored owner: every allowlisted Access email maps to
the existing single `MEMORY_USER`.

## Configure provider values

Create an Access application for the exact MCP path `/memory/oauth`. Restrict
its policy to the intended owner email. Create a separate application for the
Viz browser path that covers `/viz` and all `/viz/*` descendants, with a
different audience and the same explicit owner policy. Test the bare `/viz`
route as well as dashboard and API descendants. Do not use a browser application
audience for MCP traffic.

Copy the issuer and JWKS URL from the Access provider's advertised metadata or
from a signed assertion validated against that metadata. Configure the exact
advertised issuer value and signing-key URL. Do not derive the key URL by
assuming a particular Access hostname suffix or path.

The following values are synthetic examples on reserved example domains. The
issuer and key URL shape are placeholders; replace them with the exact values
published for your Access provider. Copy each audience from its corresponding
Access application rather than using a sample identifier.

```dotenv
ENABLE_ACCESS_MCP=true
ACCESS_ISSUER=https://access.example.net
ACCESS_JWKS_URL=https://access.example.net/keys
ACCESS_MCP_AUDIENCE=<copy the MCP application audience>
ACCESS_ALLOWED_EMAILS=owner@example.org

# Optional browser profile. Its audience must differ from the MCP audience.
ENABLE_ACCESS_VIZ=true
ACCESS_VIZ_AUDIENCE=<copy the Viz application audience>
ENABLE_VIZ=true
VIZ_PROXY_SECRET=<existing proxy secret>
```

The complete profile also requires the existing native `API_KEY` unless generic
OAuth or the isolated-development escape hatch already satisfies that
requirement. Keep `ALLOW_INSECURE_AUTH=false` for remote services. Any partial
`ACCESS_*` configuration fails startup. `ENABLE_ACCESS_VIZ=true` always
requires both `ENABLE_VIZ=true` and `VIZ_PROXY_SECRET`.

Configure the reverse proxy to preserve the Cloudflare-signed
`Cf-Access-Jwt-Assertion` header. For Viz, keep overwriting
`X-Personal-Memory-Proxy-Secret` with the server-side secret after Access has
authenticated the request. The application verifies both factors. Do not use
an email header, cookie, query parameter, or MCP tool argument as identity.

## Validate both paths

The Access MCP endpoint accepts only canonical `POST /memory/oauth` requests
without an `Origin` header. The MCP audience may be one member of the signed
audience list. A single forwarded `Authorization: Bearer oauth:<opaque>` value
may pass through as transport data; it is not used to choose an owner or
authorize the request. The endpoint rejects API-key credentials, query
credentials, duplicate assertions, browser origins, and noncanonical route
variants. Its stateless transport validates the signed assertion on every
request and creates no local MCP session.

The Viz browser endpoint requires an Access JWT with the Viz audience as its
only audience and the proxy secret. Existing review mutation Origin checks,
CSRF protections, and review session expiry continue to apply.

## Keep existing OAuth clients during migration

Run Access alongside the existing Authentik issuer while clients move. Keep the
old issuer and client registrations available until every real client has
reconnected to the Access endpoint and completed an accepted request, including
Gemini if it is used. Remove the old issuer only after that client-by-client
check. The implementation does not change production issuer state by itself.

Check these behaviors before changing client or ingress routing:

1. A native `/memory` request with `X-API-Key` or `Authorization: Bearer
   <API_KEY>` still works. Generic OAuth continues to work when enabled.
2. An Access MCP client can complete `initialize` and `tools/list` at
   `/memory/oauth` with its signed assertion.
3. A native API key, missing assertion, duplicate assertion, or browser Origin
   cannot access `/memory/oauth`; a JWKS outage fails Access requests closed.
4. Viz rejects requests with only the Access assertion or only the proxy
   secret. A browser request with both factors still passes the existing review
   Origin and CSRF checks.

## Roll back

Set `ENABLE_ACCESS_MCP=false` and `ENABLE_ACCESS_VIZ=false`, then remove the
Access-specific audience, issuer, JWKS, and allowlist values before restarting
the service. Restore the prior Authentik issuer and ingress policies if any
client has not completed the move. `/memory/oauth` returns 404, while native
`/memory` API-key and optional generic OAuth traffic remain available. With
Access Viz disabled,
Viz returns to the existing proxy-secret-only application check; restore the
prior ingress authentication policy and secret-header injection before routing
browser traffic. Remove the Access applications after no ingress route uses
them.

Keep the native API key or generic OAuth configuration in place throughout
this migration. Do not disable either profile as part of the Access rollback.
