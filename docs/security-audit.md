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
4. Automated AI evaluation is not implemented; answer attempts can remain `pending`. The conversation tables exist and curator assignments update their active owner, but the student↔curator chat/message delivery workflow is not a complete feature.
5. Automated frontend unit/e2e tests are not configured. Backend unit and integration tests plus frontend typecheck/build/CI are required for every change; integration tests use `TEST_DATABASE_URL` and must point only to a disposable database.
6. The current staff session has a 12-hour absolute expiry. A separately enforced idle timeout, MFA, and a production-grade password reset/incident-response policy should be considered before public deployment.
7. Existing administrative read routes permit curators to read authoring content (which includes expected answers and correct-choice metadata). Confirm this is an intentional content permission before providing curator accounts to untrusted users; narrow the role policy if curators should not see answer keys.
8. The app relies on the configured reverse proxy/edge to terminate TLS and enforce HTTPS/HSTS. Do not expose the backend directly on a public interface without reviewing proxy-header trust, rate limiting and transport security.

## Checks still required before production

- Implement and test MFA, including enrollment, encrypted secret storage, recovery and session invalidation.
- Run backend tests and `go vet`, migration tests, frontend `npm run typecheck` and `npm run build`, and the Compose configuration validation.
- Run integration tests only against a disposable PostgreSQL database with all migrations applied. Do not set `TEST_DATABASE_URL` to the developer database.
- Review log access for the one-time bootstrap password. If it was exposed, rotate it.
- Rehearse migration 000005 against a backup/staging copy first. Resolve case-insensitive email duplicates before deploying.
- Complete a separate security review and test the actual deployed TLS/reverse-proxy configuration.
