// ReleaseLink links a glorp version to its GitHub release page in a new tab
// (issue #825), or shows the version as plain text when the server sent no
// page for it.
export function ReleaseLink({ href, className, children }) {
	if (!href) return children;
	return (
		<a
			className={className ? `release-link ${className}` : "release-link"}
			href={href}
			target="_blank"
			rel="noopener noreferrer"
			title="Open the GitHub release page"
		>
			{children}
		</a>
	);
}
