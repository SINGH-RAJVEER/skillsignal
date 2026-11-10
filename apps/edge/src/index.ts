import { DurableObject } from "cloudflare:workers";
import { PREFIX, storage } from "./storage";

interface Env {
	BACKEND: DurableObjectNamespace<BackendContainer>;
	PARSER: DurableObjectNamespace<ParserContainer>;
	INFERENCE: DurableObjectNamespace<InferenceContainer>;
	OBJECTS: R2Bucket;
	VERSION: WorkerVersionMetadata;
	WEB_URL: string;
	DATABASE_URL: string;
	JWT_SECRET: string;
	OBJECT_STORAGE_TOKEN: string;
	OBJECT_STORAGE_READ_TOKEN: string;
	OPENROUTER_API_KEY?: string;
	RAZORPAY_KEY_ID?: string;
	RAZORPAY_KEY_SECRET?: string;
	RAZORPAY_WEBHOOK_SECRET?: string;
	ADMIN_TOKEN?: string;
}

// Requests renew the backend timeout. Workers stop themselves after
// WORKER_IDLE_EXIT_SECONDS, so their longer timeout is only a backstop that the
// renewal cron extends while they run.
const BACKEND_INACTIVITY_MS = 5 * 60 * 1000;
const WORKER_INACTIVITY_MS = 15 * 60 * 1000;
const WORKER_IDLE_EXIT_SECONDS = "300";
// A wake that lands just as a worker exits sees it running and does nothing,
// so an exit this soon after a wake restarts the Container.
const REWAKE_MS = 60_000;
const RENEW_CRON = "*/5 * * * *";
const MAINTENANCE_CRON = "0 */6 * * *";
const BACKEND_PORT = 8000;
const DRAIN_MS = 60_000;

function present(
	values: Record<string, string | undefined>,
): Record<string, string> {
	return Object.fromEntries(
		Object.entries(values).filter((entry): entry is [string, string] =>
			Boolean(entry[1]),
		),
	);
}

abstract class ProcessContainer extends DurableObject<Env> {
	private starting: Promise<void> | undefined;
	private monitoring: Promise<void> | undefined;
	private wokenAt = 0;

	protected abstract readonly name: string;
	protected abstract readonly entrypoint: string[] | undefined;
	protected abstract environment(): Record<string, string>;
	protected async waitUntilReady(): Promise<void> {}
	protected started(): void {}

	constructor(ctx: DurableObjectState, env: Env) {
		super(ctx, env);
		if (ctx.container?.running) {
			void ctx.blockConcurrencyWhile(() =>
				this.container.setInactivityTimeout(this.inactivityMs),
			);
			this.observeExit();
		}
	}

	protected get container(): Container {
		const container = this.ctx.container;
		if (!container) throw new Error("Durable Object has no Container");
		return container;
	}

	// A getter, so the base constructor already sees a subclass override.
	protected get inactivityMs(): number {
		return BACKEND_INACTIVITY_MS;
	}

	async ensureRunning(): Promise<void> {
		this.wokenAt = Date.now();
		return this.run();
	}

	// Renews a running Container without starting a stopped one. A Container
	// from an earlier release is replaced instead, so renewal cannot keep an
	// outdated image or instance type alive.
	async renew(): Promise<void> {
		if (!this.ctx.container?.running) return;
		if (
			(await this.ctx.storage.get<string>("release")) !==
			this.env.VERSION.id
		)
			return this.run();
		await this.ctx.container.setInactivityTimeout(this.inactivityMs);
	}

	private run(): Promise<void> {
		this.starting ??= this.startAndWait().finally(() => {
			this.starting = undefined;
		});
		return this.starting;
	}

	private async startAndWait(): Promise<void> {
		const container = this.container;
		const image = container.images["base"];
		if (!image) throw new Error("Container image is not configured");
		const previous = await this.ctx.storage.get<string>("release");
		const info = container.running ? await container.inspect() : null;
		// The direct Container API does not roll new images out to running instances.
		if (
			container.running &&
			(info?.image !== image || previous !== this.env.VERSION.id)
		) {
			const exited = container.monitor().catch(() => {});
			container.signal(15);
			await Promise.race([exited, scheduler.wait(DRAIN_MS)]);
			if (container.running) await container.destroy();
		}
		if (!container.running) {
			container.start({
				image,
				instance: "lite",
				enableInternet: true,
				entrypoint: this.entrypoint,
				env: this.environment(),
			});
			await this.ctx.storage.put("release", this.env.VERSION.id);
			this.started();
		}
		this.observeExit();
		await container.setInactivityTimeout(this.inactivityMs);
		await this.waitUntilReady();
	}

	private observeExit(): void {
		if (this.monitoring) return;
		this.monitoring = this.container
			.monitor()
			.catch(() =>
				console.error(`${this.name} container exited with an error`),
			)
			.finally(() => {
				this.monitoring = undefined;
				if (!this.starting && Date.now() - this.wokenAt < REWAKE_MS)
					this.ctx.waitUntil(
						this.run().catch(() =>
							console.error(
								`${this.name} container restart failed`,
							),
						),
					);
			});
		this.ctx.waitUntil(this.monitoring);
	}

	protected storageURL(): string {
		return new URL("/__storage", this.env.WEB_URL).href;
	}
}

export class BackendContainer extends ProcessContainer {
	protected readonly name = "backend";
	protected readonly entrypoint = undefined;

	protected environment(): Record<string, string> {
		return present({
			DATABASE_URL: this.env.DATABASE_URL,
			JWT_SECRET: this.env.JWT_SECRET,
			WEB_URL: this.env.WEB_URL,
			OBJECT_STORAGE_URL: this.storageURL(),
			OBJECT_STORAGE_TOKEN: this.env.OBJECT_STORAGE_TOKEN,
			RAZORPAY_KEY_ID: this.env.RAZORPAY_KEY_ID,
			RAZORPAY_KEY_SECRET: this.env.RAZORPAY_KEY_SECRET,
			RAZORPAY_WEBHOOK_SECRET: this.env.RAZORPAY_WEBHOOK_SECRET,
			ADMIN_TOKEN: this.env.ADMIN_TOKEN,
		});
	}

	// The outbox dispatcher can queue processing jobs after any start, including
	// starts from reads, deploy checks, and the maintenance cron.
	protected override started(): void {
		for (const worker of workers(this.env))
			this.ctx.waitUntil(wake(worker));
	}

	protected override async waitUntilReady(): Promise<void> {
		for (let attempt = 0; attempt < 120; attempt++) {
			try {
				const response = await this.container
					.getTcpPort(BACKEND_PORT)
					.fetch("http://container/readyz", {
						signal: AbortSignal.timeout(2_000),
					});
				await response.body?.cancel();
				if (response.ok) return;
			} catch {
				// Startup includes connecting every domain process to the database.
			}
			await scheduler.wait(500);
		}
		throw new Error("backend container did not become ready");
	}

	override async fetch(request: Request): Promise<Response> {
		await this.ensureRunning();
		const url = new URL(request.url);
		url.protocol = "http:";
		url.host = "container";
		return this.container
			.getTcpPort(BACKEND_PORT)
			.fetch(new Request(url, request));
	}
}

abstract class WorkerContainer extends ProcessContainer {
	protected override get inactivityMs(): number {
		return WORKER_INACTIVITY_MS;
	}
}

// Parsing handles untrusted files, so it runs apart from the model credential
// and with a token that cannot change stored objects.
export class ParserContainer extends WorkerContainer {
	protected readonly name = "parser";
	protected readonly entrypoint = ["/app/worker"];

	protected environment(): Record<string, string> {
		return {
			WORKER_ROLE: "parser",
			WORKER_IDLE_EXIT_SECONDS,
			DATABASE_URL: this.env.DATABASE_URL,
			JWT_SECRET: this.env.JWT_SECRET,
			OBJECT_STORAGE_URL: this.storageURL(),
			OBJECT_STORAGE_TOKEN: this.env.OBJECT_STORAGE_READ_TOKEN,
		};
	}
}

export class InferenceContainer extends WorkerContainer {
	protected readonly name = "inference";
	protected readonly entrypoint = ["/app/worker"];

	protected environment(): Record<string, string> {
		if (!this.env.OPENROUTER_API_KEY)
			throw new Error("OPENROUTER_API_KEY is not configured");
		return {
			WORKER_ROLE: "inference",
			WORKER_IDLE_EXIT_SECONDS,
			DATABASE_URL: this.env.DATABASE_URL,
			JWT_SECRET: this.env.JWT_SECRET,
			OPENROUTER_API_KEY: this.env.OPENROUTER_API_KEY,
			OBJECT_STORAGE_URL: this.storageURL(),
			OBJECT_STORAGE_TOKEN: this.env.OBJECT_STORAGE_TOKEN,
		};
	}
}

function workers(env: Env) {
	return [
		env.PARSER.getByName("parser"),
		env.INFERENCE.getByName("inference"),
	];
}

function wake(worker: ReturnType<typeof workers>[number]): Promise<void> {
	return worker
		.ensureRunning()
		.catch((error) => console.error("Worker start failed", error));
}

export default {
	async fetch(
		request: Request,
		env: Env,
		ctx: ExecutionContext,
	): Promise<Response> {
		if (new URL(request.url).pathname.startsWith(PREFIX)) {
			return storage(request, env.OBJECTS, {
				readWrite: env.OBJECT_STORAGE_TOKEN,
				readOnly: env.OBJECT_STORAGE_READ_TOKEN,
			});
		}
		// Writes may queue processing jobs, so they start the workers ahead of them.
		if (!["GET", "HEAD", "OPTIONS"].includes(request.method)) {
			for (const worker of workers(env)) ctx.waitUntil(wake(worker));
		}
		try {
			return await env.BACKEND.getByName("backend").fetch(request);
		} catch (error) {
			console.error("Backend request failed", error);
			return Response.json(
				{
					error: {
						code: "service_unavailable",
						message: "The service is temporarily unavailable.",
					},
				},
				{ status: 503 },
			);
		}
	},
	async scheduled(controller: ScheduledController, env: Env): Promise<void> {
		// PostgreSQL polling does not renew the Container inactivity timeout, so
		// running workers are renewed. The backend sweeps retention on start.
		const runs =
			controller.cron === MAINTENANCE_CRON
				? [env.BACKEND.getByName("backend").ensureRunning()]
				: controller.cron === RENEW_CRON
					? [
							env.PARSER.getByName("parser").renew(),
							env.INFERENCE.getByName("inference").renew(),
						]
					: [];
		// Run every call even when one fails, then fail the run so it is visible.
		const results = await Promise.allSettled(runs);
		const failures = results.filter(
			(result) => result.status === "rejected",
		);
		if (failures.length > 0) {
			throw new AggregateError(
				failures.map((failure) => failure.reason),
				"Scheduled Container call failed",
			);
		}
	},
} satisfies ExportedHandler<Env>;
