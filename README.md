# SkillSignal

SkillSignal provides private candidate resume checks and employer evaluations against confirmed job requirements. Candidates receive a general resume-quality report or a job-specific report with criterion outcomes. Employer organizations own jobs, requirements, submissions, invitations, and evaluation results.

Extraction and assessment use OpenRouter structured outputs with local validation. Inference requires an API key and free models with the required capabilities. Startup and periodic checks reject paid or incompatible models. Ordered fallbacks handle provider and output failures; exhausted inference fails the job and releases its unsettled point hold or weekly allowance. Application code calculates scores.

OpenRouter may route resume data to underlying providers under their own retention and training policies. This release does not restrict provider routing by Zero Data Retention or training policy.

## Implemented workflows

- Email authentication with separate employer and candidate account types, organization membership, owner/recruiter/viewer roles, and domain or email join policies.
- Private PDF, DOCX, or TXT resume checks, optional pasted or uploaded job descriptions, evidence-backed reports, suggestions, and corrected DOCX downloads. Job descriptions also accept Markdown and HTML.
- Employer jobs with versioned descriptions, recruiter-confirmed requirements, single-file and ZIP uploads, invitation links and passcodes, application windows, asynchronous processing, evidence inspection, and eligibility filters.
- Criterion scores, evidence coverage, hard gates, eligible-only top matches, and CSV exports that preserve active filters and selected columns. Viewers cannot export.
- Point ledgers, maximum-cost reservations, bounded settlement, one free independent evaluation per calendar week, Razorpay point packs in INR, and manually provisioned enterprise entitlements.
- Recoverable submission intake and delivery. Retrying an upload with the same `Idempotency-Key` reuses its documents, submission, and hold; changing its files returns a conflict. Requests without a key begin separate operations.
- Scheduled retention through the owning services, with pending delivery cancelled before derived data and source documents are removed.
- A guided demo using shared, seeded, read-only employer and candidate accounts. The tour runs without inference or point charges.

The employer review-decision screen, complete operational telemetry, labeled model-quality evaluation, and the remaining evaluation suite are still pending. Catalog compatibility and fixture tests do not establish extraction accuracy or ranking quality.

## Architecture

```
apps/
	web/        React and Vite candidate portal, employer workspace, and guided demo.
	gateway/    Public HTTP entry point and route dispatch.
	identity/   Users, authentication, organizations, members, and join policies.
	hiring/     Jobs, requirements, candidate records, invitations, submissions, and review decisions.
	documents/  Resume documents, immutable versions, private storage, and source deletion.
	evaluation/ Independent and batch reports, evidence, embeddings, scores, and exports.
	billing/    Point accounts, ledger, reservations, weekly allowance, Razorpay, and entitlements.
	worker/     Parser and inference processes selected by WORKER_ROLE.
	migrate/    One-shot PostgreSQL migrations.
	edge/       Cloudflare Worker, Container controllers, static assets, and R2 object gateway.
	supervisor/ Runs the gateway and domain services inside one Container.
packages/
	contracts/  Shared route, event, and durable-job contracts.
	backend/    Shared Go libraries.
	ui/         Shared UI components.
```

The gateway is the public backend entry point. Domain processes own authoritative tables in separate PostgreSQL schemas and authenticate their internal calls. `/healthz` checks the gateway process; `/readyz` checks every domain upstream.

PostgreSQL stores the durable queue. Parser and inference processes claim disjoint job types with `FOR UPDATE SKIP LOCKED`, commit a lease before slow work, renew it during processing, and fence result writes and point settlement against lease replacement. Hiring commits evaluation requests to an outbox with each accepted submission and retries delivery independently of the upload request.

Parser workers have no model credential and read source storage. Inference workers extract facts, embed redacted evidence, assess criteria, calculate scores, render corrected resumes, and settle holds. Local deployments use a shared private volume. Production uses private R2 through the edge object gateway; parser workers hold a read-only credential. Workers must run continuously; a process that scales to zero does not consume this polling queue.

## Run locally

Use `bun` for the TypeScript workspaces. The supported local workflow is `devenv up`, which provisions PostgreSQL, migrates the database, and starts the gateway, five domain services, parser worker, inference worker, and web client.

Create a local `.env` with a valid `OPENROUTER_API_KEY` to run inference. `.env.example` documents model lists, storage, point economics, retention, and optional payment settings. The parser process intentionally receives no OpenRouter key.

```sh
devenv up
```

The web client runs at http://localhost:3000 and the gateway at http://localhost:8000. Sign up as either account type at `/sign-up`, or choose "See how it works" for the seeded demo.

## Run with Docker Compose

Create `.env` next to `docker-compose.yml`:

```dotenv
POSTGRES_PASSWORD=change-me
JWT_SECRET=some-secret-at-least-32-characters-long
OPENROUTER_API_KEY=sk-or-...
# Optional payments:
RAZORPAY_KEY_ID=...
RAZORPAY_KEY_SECRET=...
RAZORPAY_WEBHOOK_SECRET=...
# Optional manual retention and enterprise administration:
ADMIN_TOKEN=some-admin-token
```

```sh
docker compose up --build
```

Compose applies migrations before starting the services. The parser mounts source storage read-only; inference mounts it read-write for corrected-resume artifacts. The development volume is not a production object store.

## Deploy to Cloudflare

One `skillsignal` Worker serves the web client as static assets, routes `/api/*`, `/healthz`, and `/readyz` to a backend Container, and starts separate parser and inference Containers on demand. All three use `lite` instances; the backend sleeps after five idle minutes and the workers exit once the processing pipeline is idle. The backend Container runs the gateway and five domain services on loopback because Containers have no private network between them. The account needs Workers Paid, Containers, Durable Objects, and R2.

Pull requests into protected `main` must pass `test-and-build` and `infrastructure`. Each push to `main` runs a `migrate` job, then a `deploy` job that starts only after every migration has committed. A failed migration leaves the running version in place; earlier migrations in that run stay applied, so keep them backward-compatible with the running release. The job then builds and deploys the Worker and image, and waits for `/readyz`.

The `cloudflare-production` GitHub environment holds `CLOUDFLARE_API_TOKEN`, `DATABASE_URL`, `JWT_SECRET`, `OBJECT_STORAGE_TOKEN`, `OBJECT_STORAGE_READ_TOKEN`, and optional `OPENROUTER_API_KEY`, `RAZORPAY_*`, and `ADMIN_TOKEN` secrets, with `CLOUDFLARE_ACCOUNT_ID` and `PUBLIC_URL` variables. `DATABASE_URL` must be a direct PostgreSQL 18 connection with TLS. To use a custom domain, change `WEB_URL` and add a route in `apps/edge/wrangler.jsonc`, then update `PUBLIC_URL`.

The inference Container does not start without `OPENROUTER_API_KEY`, so submitted resumes stay queued until it is set. Cloudflare limits request bodies by plan, to 100 MB on Free and Pro, which is below the application's 502 MiB batch upload limit.

## Checks

```sh
devenv shell -- go-test
devenv shell -- go-build
devenv shell -- bun run typecheck
devenv shell -- bun run build
```

Database workflow tests require explicit disposable targets. `TEST_DATABASE_URL` runs identity, hiring, documents, evaluation, and worker cases. Identity and hiring expect that target to be migrated; documents and evaluation create fresh databases and require database-creation permission. `BILLING_TEST_DATABASE_URL` and `SKILLSIGNAL_TEST_DATABASE_URL` enable billing and point-ledger concurrency cases respectively. Tests clean up their owned fixtures.

`SERVICE_INTEGRATION=1` additionally enables `TestServiceContracts` in `apps/evaluation/internal/httpapi`, which starts the five real domain binaries against a fresh database and checks seeded demo consistency, ordinary upload delivery, idempotent retry, and coordinated retention. Worker HTTP fixtures verify evidence redaction, model fallback, scoring, stale leases, and settlement. They do not prove successful live provider inference. The web client currently has no unit-test suite; verify its workflows in a browser alongside typechecking and builds.

## Documentation

`docs/specs/smart-resume-screener.md` defines product behavior. `docs/specs/service-architecture.md` describes service ownership and delivery. `docs/specs/worker.md`, `billing.md`, `resume-ingestion.md`, `retention.md`, and `in-app-demo.md` describe their implemented flows and validation limits. The architecture research document referenced by the product specification is absent from this checkout. Local planning documents remain uncommitted.
