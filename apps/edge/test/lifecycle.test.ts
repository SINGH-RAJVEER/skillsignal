import { beforeEach, expect, mock, setSystemTime, test } from "bun:test";

mock.module("cloudflare:workers", () => ({
	DurableObject: class {
		constructor(
			protected ctx: unknown,
			protected env: unknown,
		) {}
	},
}));
(globalThis as { scheduler?: unknown }).scheduler = {
	wait: () => Promise.resolve(),
};

const { BackendContainer, InferenceContainer, ParserContainer } = await import(
	"../src/index"
);

class FakeContainer {
	running = false;
	starts = 0;
	timeout = 0;
	images = { base: "image" };
	private exit: (() => void) | undefined;

	start() {
		this.running = true;
		this.starts++;
	}
	stop() {
		this.running = false;
		this.exit?.();
	}
	monitor() {
		return new Promise<void>((resolve) => {
			this.exit = resolve;
		});
	}
	async setInactivityTimeout(ms: number) {
		this.timeout = ms;
	}
	async inspect() {
		return { image: "image" };
	}
	getTcpPort() {
		return { fetch: async () => new Response(null) };
	}
}

const env = {
	VERSION: { id: "v1" },
	WEB_URL: "https://edge.test",
	OPENROUTER_API_KEY: "key",
	PARSER: { getByName: () => ({ ensureRunning: wakeWorker }) },
	INFERENCE: { getByName: () => ({ ensureRunning: wakeWorker }) },
};
let wakes: number;
async function wakeWorker() {
	wakes++;
}

function context(container: FakeContainer) {
	const stored = new Map<string, unknown>();
	return {
		container,
		storage: {
			get: async (key: string) => stored.get(key),
			put: async (key: string, value: unknown) => {
				stored.set(key, value);
			},
		},
		waitUntil: () => {},
		blockConcurrencyWhile: (run: () => Promise<unknown>) => run(),
	};
}

// Monitors stay pending while a Container runs, so drain queued work instead.
function settle() {
	return new Promise((resolve) => setTimeout(resolve, 0));
}

function create<T>(
	Class: new (ctx: never, env: never) => T,
	container: FakeContainer,
): T {
	return new Class(context(container) as never, env as never);
}

beforeEach(() => {
	wakes = 0;
	setSystemTime();
});

test("workers renew with the longer backstop timeout", async () => {
	const container = new FakeContainer();
	container.running = true;
	create(ParserContainer, container);
	await Promise.resolve();
	expect(container.timeout).toBe(15 * 60 * 1000);
});

test("renew leaves a stopped worker stopped", async () => {
	const container = new FakeContainer();
	await create(InferenceContainer, container).renew();
	expect(container.starts).toBe(0);
});

test("a worker that exits right after a wake restarts once", async () => {
	const container = new FakeContainer();
	const parser = create(ParserContainer, container);
	await parser.ensureRunning();
	container.stop();
	await settle();
	expect(container.starts).toBe(2);

	setSystemTime(Date.now() + 61_000);
	container.stop();
	await settle();
	expect(container.starts).toBe(2);
});

test("a backend start wakes both workers", async () => {
	const container = new FakeContainer();
	const backend = create(BackendContainer, container);
	await backend.ensureRunning();
	await settle();
	expect(wakes).toBe(2);

	await backend.ensureRunning();
	await settle();
	expect(wakes).toBe(2);
});
