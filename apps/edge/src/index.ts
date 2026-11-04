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

const INACTIVITY_MS = 5 * 60 * 1000;
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

	protected abstract readonly name: string;
	protected abstract readonly instance: "basic" | "standard-1";
	protected abstract readonly entrypoint: string[] | undefined;
	protected abstract environment(): Record<string, string>;
	protected async waitUntilReady(): Promise<void> {}

	constructor(ctx: DurableObjectState, env: Env) {
		super(ctx, env);
		if (ctx.container?.running) {
			void ctx.blockConcurrencyWhile(() =>
				this.container.setInactivityTimeout(INACTIVITY_MS),
			);
			this.observeExit();
		}
	}

	protected get container(): Container {
		const container = this.ctx.container;
		if (!container) throw new Error("Durable Object has no Container");
		return container;
	}

	async ensureRunning(): Promise<void> {
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
				instance: this.instance,
				enableInternet: true,
				entrypoint: this.entrypoint,
				env: this.environment(),
			});
			await this.ctx.storage.put("release", this.env.VERSION.id);
		}
		this.observeExit();
		await container.setInactivityTimeout(INACTIVITY_MS);
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
			});
		this.ctx.waitUntil(this.monitoring);
	}

	protected storageURL(): string {
		return new URL("/__storage", this.env.WEB_URL).href;
	}
}

export class BackendContainer extends ProcessContainer {
	protected readonly name = "backend";
	protected readonly instance = "standard-1" as const;
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

// Parsing handles untrusted files, so it runs apart from the model credential
// and with a token that cannot change stored objects.
export class ParserContainer extends ProcessContainer {
	protected readonly name = "parser";
	protected readonly instance = "basic" as const;
	protected readonly entrypoint = ["/app/worker"];

	protected environment(): Record<string, string> {
		return {
			WORKER_ROLE: "parser",
			DATABASE_URL: this.env.DATABASE_URL,
			JWT_SECRET: this.env.JWT_SECRET,
			OBJECT_STORAGE_URL: this.storageURL(),
			OBJECT_STORAGE_TOKEN: this.env.OBJECT_STORAGE_READ_TOKEN,
		};
	}
}

export class InferenceContainer extends ProcessContainer {
	protected readonly name = "inference";
	protected readonly instance = "basic" as const;
	protected readonly entrypoint = ["/app/worker"];

	protected environment(): Record<string, string> {
		if (!this.env.OPENROUTER_API_KEY)
			throw new Error("OPENROUTER_API_KEY is not configured");
		return {
			WORKER_ROLE: "inference",
			DATABASE_URL: this.env.DATABASE_URL,
			JWT_SECRET: this.env.JWT_SECRET,
			OPENROUTER_API_KEY: this.env.OPENROUTER_API_KEY,
			OBJECT_STORAGE_URL: this.storageURL(),
			OBJECT_STORAGE_TOKEN: this.env.OBJECT_STORAGE_TOKEN,
		};
	}
}

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		if (new URL(request.url).pathname.startsWith(PREFIX)) {
			return storage(request, env.OBJECTS, {
				readWrite: env.OBJECT_STORAGE_TOKEN,
				readOnly: env.OBJECT_STORAGE_READ_TOKEN,
			});
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
	async scheduled(_controller: ScheduledController, env: Env): Promise<void> {
		// Start every role even when one fails, then fail the run so it is visible.
		const results = await Promise.allSettled([
			env.BACKEND.getByName("backend").ensureRunning(),
			env.PARSER.getByName("parser").ensureRunning(),
			env.INFERENCE.getByName("inference").ensureRunning(),
		]);
		const failures = results.filter(
			(result) => result.status === "rejected",
		);
		if (failures.length > 0) {
			throw new AggregateError(
				failures.map((failure) => failure.reason),
				"Container keep-alive failed",
			);
		}
	},
} satisfies ExportedHandler<Env>;
