// Stored objects are bounded by the 20 MiB resume upload limit; rendered
// documents stay well below this. Requests stream to keep Worker memory flat.
const MAX_BYTES = 64 << 20;
const KEY = /^[A-Za-z0-9_-][A-Za-z0-9._-]*(\/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$/;
export const PREFIX = "/__storage/";

export interface StorageCredentials {
	readWrite: string;
	readOnly: string;
}

// Containers reach this route over the public Worker address. Unauthenticated
// callers cannot distinguish it from an unknown path.
export async function storage(
	request: Request,
	bucket: R2Bucket,
	credentials: StorageCredentials,
): Promise<Response> {
	const access = await grantedAccess(request, credentials);
	if (access === "none") return new Response(null, { status: 404 });

	const key = new URL(request.url).pathname.slice(PREFIX.length);
	if (!KEY.test(key)) return new Response(null, { status: 400 });

	if (request.method === "GET") {
		const object = await bucket.get(key);
		if (!object) return new Response(null, { status: 404 });
		return new Response(object.body, {
			headers: {
				"Content-Type": "application/octet-stream",
				"Cache-Control": "no-store",
			},
		});
	}
	if (access !== "write") return new Response(null, { status: 403 });
	if (request.method === "DELETE") {
		await bucket.delete(key);
		return new Response(null, { status: 204 });
	}
	if (request.method !== "PUT") return new Response(null, { status: 405 });

	const length = request.headers.get("Content-Length");
	if (!length) return new Response(null, { status: 411 });
	const size = Number(length);
	if (!Number.isSafeInteger(size) || size < 0 || size > MAX_BYTES) {
		return new Response(null, { status: 413 });
	}
	await bucket.put(key, request.body ?? new Uint8Array());
	return new Response(null, { status: 204 });
}

async function grantedAccess(
	request: Request,
	credentials: StorageCredentials,
): Promise<"write" | "read" | "none"> {
	const presented = request.headers.get("Authorization") ?? "";
	if (await matches(presented, credentials.readWrite)) return "write";
	if (await matches(presented, credentials.readOnly)) return "read";
	return "none";
}

async function matches(presented: string, token: string): Promise<boolean> {
	if (token.length < 32) return false;
	// Compare fixed-length digests instead of returning after the first mismatch.
	const encoder = new TextEncoder();
	const [actual, expected] = await Promise.all([
		crypto.subtle.digest("SHA-256", encoder.encode(presented)),
		crypto.subtle.digest("SHA-256", encoder.encode(`Bearer ${token}`)),
	]);
	const left = new Uint8Array(actual);
	const right = new Uint8Array(expected);
	let difference = 0;
	for (let index = 0; index < left.length; index++) {
		difference |= (left[index] ?? 0) ^ (right[index] ?? 0);
	}
	return difference === 0;
}
