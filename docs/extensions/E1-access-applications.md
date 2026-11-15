# E1: Access Application Management via Labels

## Issue Title
**Manage Cloudflare Access applications from container labels**

## Why
v2.1.1.7 and the `dockflare.*` dialect can protect a hostname with a Zero Trust Access app. The core build deliberately blocks such rules (§3.4); this extension removes the block for the keys it implements.

## Scope
- Labels: `access.policy` (`bypass`, `authenticate`, `default_tld`), `access.name` (default `DockFlare-<hostname>`; **adopt existing apps by name or domain**), `access.session_duration` (default `24h`), `access.app_launcher_visible`, `access.allowed_idps`, `access.custom_rules` (JSON array; each entry's `action` (`allow|deny|bypass|non_identity`, default `allow`) selects the policy decision, all other keys are Access include-rules; group entries by action).
- Decide and document how `access.group` (reusable policy reference) and `access.email` (`a@x.com,@x.com`) map; implement or keep blocked.
- Delete the Access app when its rule expires; update in place when labels change; record app/policy IDs in state.
- Remove the fail-closed block **only** for implemented keys.
- Token scope: add *Access: Apps and Policies: Edit*.

## Acceptance
httptest-backed tests for create, adopt, update-in-place, delete-on-expiry, and shrinking of the blocked set; manual verification on a scratch app against the test tunnel.

## Notes
The SDK's Access types are large unions; keep them inside `internal/cf` behind the existing interface (a thin raw-request fallback in that package is acceptable; record it in DECISIONS.md).
