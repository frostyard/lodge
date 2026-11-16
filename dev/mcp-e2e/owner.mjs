// The owner's part of a sign-in or an access request, as a browser does it:
// open the consent page, read its CSRF token and hash, and approve it. The
// development build gives every loopback peer the e2e identity
// (internal/e2eid), which dev/mcp-e2e/run.sh makes an admin. Only the
// development build answers these without a tailnet; a release never does.

const csrfField = /name="csrf" value="([^"]+)"/;
const hashField = /name="hash" value="([^"]+)"/;

// consentPath follows an authorization URL to its consent page: lodge
// answers /oauth/authorize with a 303 to /oauth/consent/<id>. A consent URL
// (from request_access) is its own page.
async function consentPath(origin, url) {
	const u = new URL(url, origin);
	if (u.pathname.startsWith("/oauth/consent/")) {
		return u.pathname;
	}
	const res = await fetch(u, { redirect: "manual" });
	const loc = res.headers.get("location") ?? "";
	if (res.status !== 303 || !loc.startsWith("/oauth/consent/")) {
		throw new Error(`authorize: HTTP ${res.status}, Location ${JSON.stringify(loc)}: ${await res.text()}`);
	}
	return loc;
}

// approve approves the consent page that url leads to. fields are the form's
// other members (principal and scope for a sign-in; item and path.<i>.<sel>
// for an access request, else every item as asked). It returns the Location
// of lodge's answer: the client's redirect URI with the code for a sign-in,
// or lodge's grants page for an access request.
export async function approve(origin, url, fields = {}) {
	const path = await consentPath(origin, url);
	const page = await fetch(new URL(path, origin), {
		headers: { Origin: origin, "Sec-Fetch-Site": "same-origin" },
	});
	const body = await page.text();
	const csrf = csrfField.exec(body)?.[1];
	const hash = hashField.exec(body)?.[1];
	if (page.status !== 200 || !csrf || !hash) {
		throw new Error(`consent page ${path}: HTTP ${page.status}: ${body.slice(0, 300)}`);
	}
	const form = new URLSearchParams({ csrf, hash, decision: "approve" });
	for (const [name, value] of Object.entries(fields)) {
		for (const v of [value].flat()) {
			form.append(name, v);
		}
	}
	const res = await fetch(new URL(path, origin), {
		method: "POST",
		redirect: "manual",
		headers: {
			Origin: origin,
			"Sec-Fetch-Site": "same-origin",
			"Content-Type": "application/x-www-form-urlencoded",
		},
		body: form,
	});
	const loc = res.headers.get("location");
	if (res.status !== 303 || !loc) {
		throw new Error(`approve ${path}: HTTP ${res.status}: ${await res.text()}`);
	}
	return loc;
}

// finish sends the browser to the client's redirect URI, as a browser
// follows lodge's 303 after an approval.
export async function finish(location) {
	const res = await fetch(location);
	if (!res.ok) {
		throw new Error(`redirect ${location}: HTTP ${res.status}: ${await res.text()}`);
	}
}
