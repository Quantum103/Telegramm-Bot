# Security review and production blockers

Review basis: repository state at `71d31090b9f982bfd5cc9feda615cd6ad55821c6` plus the changes in the staff/student management feature branch. This document describes code paths, not a certification or penetration-test result.

## Controls implemented in the application

- Staff passwords use salted PBKDF2-HMAC-SHA256 with 600,000 iterations; the database stores password hashes, not clear passwords.
- Staff sessions use cryptographically random opaque tokens. Only SHA-256 digests and the CSRF-token digest are stored server-side. The session cookie is HttpOnly, SameSite=Strict and Secure in the production server constructor, and responses are marked no-store.
- Staff writes require a same-origin Origin header and a CSRF token checked against the digest stored for that session. Staff routes are behind session middleware and role checks. The admin router defaults to administrator for writes; curated student listing has additional actor-specific filtering in the repository query.
- Failed-login limiting uses Redis and the API fails closed if the shared limiter is unavailable. This protects the current single-instance/Compose deployment; rate-limiter behaviour should be reviewed again before horizontal scaling or a different proxy topology.
- Staff creation and role/curator mutations are transactional and produce audit events. Role changes, password changes/resets and staff status changes revoke active sessions. The repository checks actor role in addition to HTTP middleware.
- New employee accounts receive a high-entropy temporary password, whose hash is stored in PostgreSQL. The raw temporary password is returned only by the creating/resetting HTTP request, is not logged, and is not stored in a durable server table. `must_change_password` makes normal staff routes unavailable until the user changes it. Password change invalidates active sessions.
- Case-insensitive duplicate email addresses are rejected by migration `000005_staff_management` before adding a unique index on `lower(email)`; if duplicates exist, migration fails closed for manual reconciliation.
- Curator access to student lists is filtered server-side to active assignments. Transfer/unassignment is performed in a transaction, the assignment history is preserved, active conversation ownership is synchronized, and changes are audited.
- DOCX import has bounds on uploaded/compressed/uncompressed data and archive-entry count; parser code rejects suspicious archive entries and strict XML constructs rather than extracting arbitrary files.
- SQL uses parameterized queries, and important relations use foreign keys, unique constraints and transactional updates.

## Known production blockers and limits

1. **MFA/2FA is not implemented. Do not expose the admin panel to the public internet or call this project production-ready until MFA is implemented with a maintained library/protocol, recovery procedures are reviewed, and independent security testing is performed.**
2. The bootstrap administrator can still be initialized from environment configuration. If `BOOTSTRAP_ADMIN_PASSWORD` is explicitly set, it is treated as an administrator-selected password and is not forced to rotate. Keep this secret out of logs and deployment manifests; do not rely on bootstrap defaults for a public production system.
3. Initial passwords are intentionally delivered out of band by an administrator; there is no email/SMS invitation channel. This avoids introducing an unconfigured email dependency but requires a private hand-off and an immediate password change.
4. Automated AI evaluation is not implemented; answer attempts can remain `pending`. Student↔curator messaging is implemented through a shared PostgreSQL conversation history: Telegram and Mini App messages use the same conversation, Mini App sends use idempotency keys, and Telegram-authenticated endpoints expose history and unread counts. Mini App refreshes messages and unread counts by polling; this is near-real-time polling, not a push/WebSocket guarantee.
5. Automated frontend unit/e2e tests are not configured. Backend unit and integration tests plus frontend typecheck/build/CI are required for every change; integration tests use `TEST_DATABASE_URL` and must point only to a disposable database.
6. The current staff session has a 12-hour absolute expiry. A separately enforced idle timeout, MFA, and a production-grade password reset/incident-response policy should be considered before public deployment.
7. Curators previously could read internal authoring questions, including expected answers, grading criteria and correct-choice metadata, through the administrative questions endpoint. The endpoint now explicitly requires the administrator role; curators must use the assignment-scoped review API.
8. The app relies on the configured reverse proxy/edge to terminate TLS and enforce HTTPS/HSTS. Do not expose the backend directly on a public interface without reviewing proxy-header trust, rate limiting and transport security.

## Mini App security and operational notes

- Mini App API requests validate Telegram Web App `initData` with HMAC-SHA256, reject duplicate parameters, enforce a 24-hour maximum age and reject timestamps more than one minute in the future.
- Mini App JSON writes reject unknown fields and trailing JSON, and are bounded to 16 KiB. Conversation message text and idempotency keys have additional domain validation.
- Mini App API responses are marked `no-store`; the Nginx frontend marks HTML entry points `no-store` and applies basic browser security headers.
- The Mini App currently polls for conversation updates (5 seconds while the chat is open; 10 seconds for unread counts elsewhere). This creates periodic authenticated reads and should be load-tested at expected concurrent-user scale. It does not provide push delivery guarantees.
- These controls do not replace per-user API rate limiting, MFA for staff, a reviewed TLS/reverse-proxy deployment, or independent penetration testing.

- CI now includes `govulncheck` for reachable Go vulnerabilities and `npm audit` for high/critical frontend dependency advisories.
- The frontend repository currently has no committed npm lockfile. CI's npm audit therefore resolves the dependency tree afresh; this detects advisories but does not provide reproducible, integrity-pinned transitive dependencies. Add and commit `web/package-lock.json`, then switch CI to `npm ci` and a normal locked `npm audit`.

## Whole-application code review — 2026-11-09

Review scope: Go HTTP handlers, repositories/services, Telegram update handling and message formatting, Mini App/React rendering and requests, DOCX parsing, authentication/authorization, Docker Compose and the Nginx proxy configuration. This was a static review of the repository, not a live penetration test or an assessment of the deployed host.

### Findings and status

- **SQL injection:** no user-controlled SQL construction was found in the repository code inspected. PostgreSQL statements are static SQL with positional parameters; the few statement collections in the authoring repository are hard-coded constants. This is a source-review result, not proof that every query path is vulnerability-free.
- **Command injection:** no shell/process execution path was found in the backend application code inspected.
- **XSS:** no direct innerHTML, dangerouslySetInnerHTML, document.write or eval sink was found in the frontend source inspected. React text interpolation uses its normal escaping; Telegram HTML output uses an HTML-escaping builder for dynamic text. Continue to treat all imported, student and staff-entered text as untrusted and avoid adding raw HTML rendering.
- **Authorization / answer-key disclosure:** the administrative questions endpoint returns expected answers and correct-option metadata. It now explicitly requires the administrator role; curators are denied and must use the assignment-scoped course-review API. A regression test verifies that a curator request is rejected before the authoring service is called.
- **DOCX upload:** the upload is restricted to one .docx, bounded to 8 MiB plus multipart overhead, and ZIP/XML parsing has entry, per-entry, total-uncompressed and extracted-text limits. The parser rejects path traversal, symlinks, encrypted entries, macro indicators and XML directives; it does not extract files to disk.
- **Telegram trust boundary:** callback processing checks that the callback sender matches the private chat, and learning/conversation operations use the authenticated Telegram user/student ID plus repository ownership and assignment checks. Telegram Mini App initData is HMAC-validated, duplicate parameters are rejected, and age is bounded.
- **Request abuse / denial of service:** staff login has a Redis-backed failure/concurrency limiter. There is no general per-IP or per-user request limiter covering public catalogue routes, Mini App routes, or authenticated write operations. A malicious client can therefore generate avoidable database and CPU load; add a shared rate-limit policy at the trusted edge and/or application layer before public exposure. Do not trust client-supplied forwarding headers unless the backend is reachable only through a proxy that overwrites them.
- **Database privilege boundary — high-priority deployment issue:** Compose uses the same POSTGRES_USER for database initialization, the backend runtime connection and the migration tool. With the standard PostgreSQL image, the initialization user is the database superuser. A SQL injection or backend compromise would therefore have a much larger blast radius than necessary. Introduce a separate least-privilege runtime role (DML on only the application schema, required sequence privileges, no role/database creation or superuser) and a separate migration/owner credential. Plan this as a controlled change for existing persistent volumes; changing environment variables alone does not alter roles in an already-initialized PostgreSQL data directory.
- **Transport / perimeter:** Compose binds service ports to loopback, which is appropriate for a local single-host deployment, but Nginx and the backend themselves serve HTTP. Public deployment requires a reviewed TLS-terminating reverse proxy with HTTPS/HSTS, restrictive firewall rules, trusted proxy-header handling and no direct public access to the backend or database.
- **Staff authentication:** staff passwords use PBKDF2 and sessions use random opaque tokens with CSRF/Origin checks and a 12-hour absolute expiry. MFA and an idle timeout are still absent; account compromise remains a significant risk for a public staff panel.
- **Frontend dependency integrity:** there is no committed web/package-lock.json; the build uses npm install. CI runs dependency vulnerability checks, but without a lockfile transitive versions are not reproducibly pinned. Commit a lockfile and use npm ci.
- **Vulnerability scanners:** CI runs govulncheck and npm audit; their result for the current commit must be checked in GitHub Actions. A clean scan only covers known advisories and does not replace source review, runtime hardening or penetration testing.

### Production decision

The reviewed code contains several meaningful protections, and no obvious SQL-injection, command-injection or unsafe-HTML sink was identified in the inspected paths. **Do not interpret this as “users cannot hack the app” or as a security certification.** Before exposing the platform publicly, prioritize the least-privilege PostgreSQL runtime role, general request rate limiting, MFA for staff, locked frontend dependencies, and a verified HTTPS/reverse-proxy deployment. Then perform an independent authenticated and unauthenticated penetration test against staging.

## Checks still required before production

- Implement and test MFA, including enrollment, encrypted secret storage, recovery and session invalidation.
- Run backend tests and `go vet`, migration tests, frontend `npm run typecheck` and `npm run build`, and the Compose configuration validation.
- Run integration tests only against a disposable PostgreSQL database with all migrations applied. Do not set `TEST_DATABASE_URL` to the developer database.
- Review log access for the one-time bootstrap password. If it was exposed, rotate it.
- Rehearse migration 000005 against a backup/staging copy first. Resolve case-insensitive email duplicates before deploying.
- Complete a separate security review and test the actual deployed TLS/reverse-proxy configuration.
