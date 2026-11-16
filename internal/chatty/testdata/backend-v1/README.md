# Shared D2/D4 Fixtures

Normative contract: `chatty/docs/design/backend-onboarding/CONTRACT-V1.md`,
commit `51cf63f80b7dd99bb170759d02e54761f119c444`, including the version amendment
`{installed, running}` and authorization only through whoami + local readers.
All data is synthetic.

- `healthy-unknown-version.json`: complete valid document; shared LocalAI and advertised model, without claiming a loaded model or functional inference. Unknown versions do not lower health.
- `mutations.json`: independent input cases against this baseline. `path` is an array of keys; numeric elements index arrays. `value` replaces the field, including with `null`; `remove: true` removes only that field. An empty `error` means a valid document. Each negative case changes exactly one input field. The Go test applies and verifies these same bytes.
- `http-errors.json`: complete example HTTP error responses, never snapshots. HEAD preserves status/headers and has no body. This 404 is for an unknown reserved route; health may not even exist on an older port, and consumers must not invent component absence.
- `backend-config.json`: **local owner configuration**, not a protocol document. Includes all ten IDs and only synthetic absent paths, without service URLs. Suitable for CLI smoke tests without reading personal services/configuration/secrets. The 30-second validity is a fixture, not a mandatory polling frequency.
- `stale.json`: valid snapshot changing only baseline `validUntil` to `2026-10-30T21:00:00.5Z`. At reading time `2026-10-30T21:00:01Z`, all reasons become `stale`; at `observedAt`, it remains current. Not a schema error.
- `model-missing.json`: keeps the hub healthy and changes only the model observation to `absent/modelUnavailable`, without claiming loading or inference. `catalog-without-model.json` is the synthetic HTTP body producing this projection, served by the test with the hub's own credential.
- `changed-origin.json`: changes **only** `sourceOrigin` to `https://other.synthetic.example`, still structurally valid. Consumers must compare it with the trusted request origin, never follow the document as discovery.
- `changed-installation.json`: changes **only** `installationID` to `33333333-3333-4333-8333-333333333333`, still a valid UUID. Consumers must invalidate observations bound to the previous installation. No origin change is mixed into this case.

`TestBackendSharedScenarioVectors` reads these files, checks each case's exact
difference against the baseline and executes decoding/expiry. It also compares
the missing-model vector with real synthetic HTTP collection. Changed origin
or installation is not a producer structural error: these are bindings D4 checks
against its own trusted context. There is no additional session identity beyond
whoami + the owner's allowlist.

HTTP tests generate catalog responses 401/403/404, empty catalogs, invalid
responses and unreachable endpoints without replacing these observations with
absence. They also exercise simultaneous IPv4/IPv6 HTTP and TLS/HTTP2 in
`runDoor`, with credentials exclusive to the synthetic upstreams. Expected
session identity/origin belong to the consumer's trusted context, never to
destinations extracted from these fixtures. No fixture authorizes a connection
or installation action.
