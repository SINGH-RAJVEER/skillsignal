import { beforeEach, describe, expect, test } from "bun:test";
import { storage } from "../src/storage";

const credentials = { readWrite: "w".repeat(32), readOnly: "r".repeat(32) };
let objects: Map<string, string>;

const bucket = {
	get: async (key: string) => {
		const value = objects.get(key);
		return value === undefined ? null : { body: value };
	},
	put: async (key: string, body: ReadableStream | null) => {
		objects.set(key, await new Response(body).text());
	},
	delete: async (key: string) => {
		objects.delete(key);
	},
} as unknown as R2Bucket;

function call(
	method: string,
	path: string,
	token?: string,
	body?: string,
): Promise<Response> {
	const headers = new Headers();
	if (token) headers.set("Authorization", `Bearer ${token}`);
	if (body !== undefined) headers.set("Content-Length", String(body.length));
	return storage(
		new Request(`https://edge.test/__storage/${path}`, {
			method,
			headers,
			body,
		}),
		bucket,
		credentials,
	);
}

describe("object gateway", () => {
	beforeEach(() => {
		objects = new Map([["resumes/a.pdf", "resume"]]);
	});

	test("hides objects from unauthenticated callers", async () => {
		expect((await call("GET", "resumes/a.pdf")).status).toBe(404);
		expect(
			(await call("GET", "resumes/a.pdf", "x".repeat(32))).status,
		).toBe(404);
		expect(
			(await call("PUT", "resumes/b.pdf", undefined, "x")).status,
		).toBe(404);
		expect(objects.has("resumes/b.pdf")).toBe(false);
	});

	test("lets the read token read and nothing else", async () => {
		const read = await call("GET", "resumes/a.pdf", credentials.readOnly);
		expect(read.status).toBe(200);
		expect(await read.text()).toBe("resume");
		expect(
			(await call("PUT", "resumes/b.pdf", credentials.readOnly, "x"))
				.status,
		).toBe(403);
		expect(
			(await call("DELETE", "resumes/a.pdf", credentials.readOnly))
				.status,
		).toBe(403);
		expect(objects.has("resumes/a.pdf")).toBe(true);
	});

	test("writes and deletes with the read-write token", async () => {
		expect(
			(await call("PUT", "resumes/b.pdf", credentials.readWrite, "new"))
				.status,
		).toBe(204);
		expect(objects.get("resumes/b.pdf")).toBe("new");
		expect(
			(await call("DELETE", "resumes/b.pdf", credentials.readWrite))
				.status,
		).toBe(204);
		expect(objects.has("resumes/b.pdf")).toBe(false);
	});

	test("rejects malformed keys and oversized writes", async () => {
		expect(
			(await call("GET", "%2e%2e/secret", credentials.readWrite)).status,
		).toBe(400);
		expect(
			(await call("GET", "resumes//a.pdf", credentials.readWrite)).status,
		).toBe(400);
		const oversized = new Request(
			"https://edge.test/__storage/resumes/big.pdf",
			{
				method: "PUT",
				headers: {
					Authorization: `Bearer ${credentials.readWrite}`,
					"Content-Length": String(65 << 20),
				},
			},
		);
		expect((await storage(oversized, bucket, credentials)).status).toBe(
			413,
		);
	});

	test("refuses every request when a credential is too short", async () => {
		const weak = { readWrite: "", readOnly: "" };
		const request = new Request(
			"https://edge.test/__storage/resumes/a.pdf",
			{
				headers: { Authorization: "Bearer " },
			},
		);
		expect((await storage(request, bucket, weak)).status).toBe(404);
	});
});
