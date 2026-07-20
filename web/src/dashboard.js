export function deliveryLabel(snapshot) {
	if (snapshot.UseWebhooks) return "push";
	const interval = snapshot.Interval
		? `${snapshot.Interval / 1_000_000_000}s`
		: "—";
	return `polling every ${interval}`;
}
