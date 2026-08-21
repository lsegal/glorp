export function deliveryLabel(snapshot) {
	if (snapshot.UseWebhooks) return "push";
	const interval = snapshot.Interval
		? `${snapshot.Interval / 1_000_000_000}s`
		: "—";
	return `polling every ${interval}`;
}

export function jobActionAvailability(status) {
	return {
		retry: status === "failed",
		stop: status === "active",
	};
}

export async function submitJobAction(job, action) {
	const response = await fetch("/api/jobs/action", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ action, target: job.Target, number: job.Number }),
	});
	if (!response.ok) {
		throw new Error((await response.text()) || `HTTP ${response.status}`);
	}
}
