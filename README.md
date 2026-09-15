# SkillSignal

A resume screening tool with two sides that never mix. Candidates run private
checks on their own resumes and get a readiness score, suggestions, and a
corrected DOCX built only from facts their document actually contains.
Employers create roles, confirm the criteria that matter, upload or invite
resumes, and review ranked, evidence-cited evaluations against those criteria.

Every score traces back to quoted text from the source document. The model
stages, extraction and requirement assessment, run through OpenRouter with
strict JSON schemas, and their output is validated locally before anything is
stored. If no API key is configured, or a model call fails, everything falls
back to deterministic parsing and scoring. The system never lets a model erase
evidence that deterministic matching already found.

## Scope

What works today:

- Email auth with separate employer and candidate account types.
- Candidate private evaluations: PDF/DOCX/TXT upload, optional job description,
  scored report, suggestions, corrected resume download.
- Employer workspace: organizations with email-domain join policies, members
  with owner/recruiter/viewer roles, jobs with draft criteria extracted from
  descriptions, criteria confirmation into immutable versions, single and ZIP
  batch resume uploads, candidate invitations by link or passcode gated on an
  application window.
- Evaluations: deterministic skill matching against an O*NET-backed vocabulary,
  OpenRouter fact extraction and block embeddings for semantic evidence, with a
  content-hash cache so repeated blocks cost nothing, per-requirement
  assessment with cited evidence, weighted scores, hard-gate eligibility, CSV
  export with selectable columns.
- Points billing: ledgers for user and organization accounts, one free
  evaluation per calendar week for independent users, one-time Razorpay
  payments in INR, employer batch billing, and manually provisioned enterprise
  entitlements.
- Retention: resumes and everything derived from them expire after a
  configurable number of days. The API sweeps expired data on a schedule, and
  an operator can trigger a pass manually.
- Tracing and metrics through OpenTelemetry when an OTLP endpoint is set.

`POST /api/demo/session` seeds a pre-populated workspace and returns a token
for either a demo employer or a demo candidate.

What is not built yet: the employer review-decision screen, the full set of
cost and latency telemetry, calibration against labeled data, and the
remaining evaluation test suite. The specs in `docs/specs/` describe the full
intended product.

## Layout

```
apps/
	web/        React 19 + Vite candidate portal and employer workspace.
	gateway/    Public HTTP entry point and route dispatch.
	identity/   Users, accounts, employer organizations, members, and join policies.
	hiring/     Jobs, requirements, candidate records, invitations, submissions, and review decisions.
	documents/  Resume documents, immutable versions, storage metadata, and retention.
	evaluation/ Independent and batch evaluations, evidence, embeddings, scores, and ranking.
	billing/    Point accounts, ledger, reservations, weekly allowance, Razorpay, and entitlements.
	worker/     Parser and inference worker implementation selected by WORKER_ROLE.
	migrate/    One-shot PostgreSQL schema migration command.
packages/
	contracts/ Versioned route, event, and durable-job contracts shared across processes.
	backend/   Reusable server-side packages shared by backend processes.
	ui/        Shared UI component package used by web.
docs/
	specs/      Product behavior and service ownership.
	research/   Provider constraints and architecture research.
```

The gateway is the only public backend process. Domain processes reject routes they do not own. PostgreSQL keeps one physical cluster but separates authoritative tables into `identity`, `hiring`, `documents`, `evaluation`, `billing`, and `platform` schemas. Domain processes connect with only their own schema and the platform delivery schema in their search path.

Parser and inference workers claim different durable job types. Parser workers receive no OpenRouter credential. Inference workers handle model extraction, embeddings, assessment, scoring, and corrected-resume rendering. The worker skill corpus is generated at `apps/worker/internal/vocab/skills_corpus.json`.

## Run with Docker

Requires Docker and a `.env` file next to `docker-compose.yml` with:

```
POSTGRES_PASSWORD=change-me
JWT_SECRET=some-secret-at-least-32-characters-long
# optional, enables the model stages:
OPENROUTER_API_KEY=sk-or-...
# optional, enables Razorpay point packs and webhooks:
RAZORPAY_KEY_ID=...
RAZORPAY_KEY_SECRET=...
# optional, enables admin endpoints (retention sweep, enterprise provisioning):
ADMIN_TOKEN=some-admin-token
```

`.env.example` documents every variable with its default. Then:

```sh
docker compose up --build
```

Compose runs a one-shot migrator before the domain processes and workers start. The parser mounts source storage read-only. The inference worker mounts it read-write because corrected resumes are output artifacts. When startup finishes, the web client is at http://localhost:3000 and the gateway is at http://localhost:8000. `/healthz` checks the gateway process; `/readyz` checks every domain upstream.

## Run locally

The supported local workflow is `devenv up`. It provisions PostgreSQL, applies migrations, and starts the gateway, five domain processes, parser worker, inference worker, and Vite client.

1. Create a database, then export connection settings:

```sh
export DATABASE_URL="postgresql://postgres:password@localhost:5432/skillsignal"
export JWT_SECRET="some-secret-at-least-32-characters-long"
export STORAGE_ROOT=".local-storage"
export OPENROUTER_API_KEY="sk-or-..."   # optional
```

`.env.example` documents every variable with its default.

2. Start the complete topology:

```sh
devenv up
```

Sign up as an employer at `/sign-up` to reach the workspace, or as a
candidate to run private checks.

## Tests

```sh
CGO_ENABLED=0 go test ./packages/contracts/...
CGO_ENABLED=0 go test ./packages/backend/...
CGO_ENABLED=0 go test ./apps/migrate/...
CGO_ENABLED=0 go test ./apps/gateway/...
CGO_ENABLED=0 go test ./apps/worker/...
CGO_ENABLED=0 go test ./apps/identity/... ./apps/hiring/... ./apps/documents/... ./apps/evaluation/... ./apps/billing/...
```

Or run `go-test` inside the devenv shell. The web app has no unit tests yet; check it with `bun run typecheck` and `bun run lint`.

## Where to read more

`docs/specs/smart-resume-screener.md` is the product specification.
`docs/specs/service-architecture.md` defines domain ownership, route routing, queue contracts, and database schemas.
`docs/research/resume-screener-implementation.md` covers why the pipeline is
shaped this way: parser choices, OpenRouter routing and privacy settings,
scoring design, and the legal constraints around automated hiring tools.
