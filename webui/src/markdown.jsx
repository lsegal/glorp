// Markdown renders the agent's prose in the transcript (issue #863). Agent
// output is untrusted, so it is parsed into React elements and never into an
// HTML string: React escapes every piece of text, raw HTML in the source is
// shown as the text it is, and links keep only http, https, and mailto URLs.

const fencePattern = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)[^`]*$/;
const headingPattern = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const rulePattern = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const quotePattern = /^ {0,3}>\s?/;
const itemPattern = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const tableRulePattern = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

// safeHref is url when it is an http, https, or mailto link, otherwise "".
export function safeHref(url) {
	const href = url.trim();
	return /^(https?:\/\/|mailto:)/i.test(href) ? href : "";
}

function indentOf(text) {
	return text.match(/^\s*/)[0].replace(/\t/g, "    ").length;
}

function blockStart(line) {
	return (
		fencePattern.test(line) ||
		headingPattern.test(line) ||
		rulePattern.test(line) ||
		quotePattern.test(line) ||
		itemPattern.test(line)
	);
}

function tableCells(line) {
	return line
		.trim()
		.replace(/^\|/, "")
		.replace(/\|$/, "")
		.split(/(?<!\\)\|/)
		.map((cell) => cell.trim());
}

// parseBlocks splits markdown source into block nodes.
export function parseBlocks(source) {
	const lines = source.replace(/\r\n?/g, "\n").split("\n");
	const blocks = [];
	let i = 0;
	while (i < lines.length) {
		const line = lines[i];
		if (line.trim() === "") {
			i++;
			continue;
		}
		const fence = line.match(fencePattern);
		if (fence) {
			const body = [];
			i++;
			while (
				i < lines.length &&
				!(
					lines[i].trim().startsWith(fence[1][0].repeat(fence[1].length)) &&
					lines[i].trim().replace(/[`~]/g, "") === ""
				)
			) {
				body.push(lines[i]);
				i++;
			}
			i++;
			blocks.push({ type: "code", lang: fence[2], text: body.join("\n") });
			continue;
		}
		const heading = line.match(headingPattern);
		if (heading) {
			blocks.push({
				type: "heading",
				level: heading[1].length,
				text: heading[2],
			});
			i++;
			continue;
		}
		if (rulePattern.test(line)) {
			blocks.push({ type: "rule" });
			i++;
			continue;
		}
		if (quotePattern.test(line)) {
			const body = [];
			while (i < lines.length && quotePattern.test(lines[i])) {
				body.push(lines[i].replace(quotePattern, ""));
				i++;
			}
			blocks.push({ type: "quote", blocks: parseBlocks(body.join("\n")) });
			continue;
		}
		const item = line.match(itemPattern);
		if (item) {
			const base = indentOf(item[1]);
			const ordered = /\d/.test(item[2]);
			const list = {
				type: "list",
				ordered,
				start: ordered ? Number.parseInt(item[2], 10) : 1,
				items: [],
			};
			let current = null;
			while (i < lines.length) {
				const next = lines[i];
				const match = next.match(itemPattern);
				if (
					match &&
					indentOf(match[1]) <= base + 1 &&
					/\d/.test(match[2]) === ordered
				) {
					current = [match[3]];
					list.items.push(current);
					i++;
					continue;
				}
				if (next.trim() === "") {
					// A blank line ends the list unless the list goes on after it.
					let ahead = i + 1;
					while (ahead < lines.length && lines[ahead].trim() === "") ahead++;
					const after = lines[ahead];
					const continues =
						after !== undefined &&
						(indentOf(after) > base ||
							(itemPattern.test(after) &&
								indentOf(after.match(itemPattern)[1]) <= base + 1));
					if (!continues) break;
					current.push("");
					i++;
					continue;
				}
				if (indentOf(next) > base || !blockStart(next)) {
					current.push(next.slice(Math.min(indentOf(next), base + 2)));
					i++;
					continue;
				}
				break;
			}
			list.items = list.items.map((body) => parseBlocks(body.join("\n")));
			blocks.push(list);
			continue;
		}
		if (
			line.includes("|") &&
			i + 1 < lines.length &&
			tableRulePattern.test(lines[i + 1]) &&
			lines[i + 1].includes("-")
		) {
			const header = tableCells(line);
			const rows = [];
			i += 2;
			while (i < lines.length && lines[i].includes("|")) {
				rows.push(tableCells(lines[i]));
				i++;
			}
			blocks.push({ type: "table", header, rows });
			continue;
		}
		const body = [line];
		i++;
		while (
			i < lines.length &&
			lines[i].trim() !== "" &&
			!blockStart(lines[i])
		) {
			body.push(lines[i]);
			i++;
		}
		blocks.push({ type: "paragraph", text: body.join("\n") });
	}
	return blocks;
}

const inlineRules = [
	{ type: "escape", pattern: /^\\([\\`*_{}[\]()#+\-.!|~<>])/ },
	{ type: "code", pattern: /^(`+)(?!`)([\s\S]*?[^`])\1(?!`)/ },
	{
		type: "link",
		pattern:
			/^\[((?:\[[^\]]*\]|[^[\]])*)\]\(\s*<?([^\s()<>]*(?:\([^\s()]*\)[^\s()<>]*)*)>?(?:\s+"[^"]*")?\s*\)/,
	},
	{ type: "autolink", pattern: /^<((?:https?:\/\/|mailto:)[^\s<>]+)>/i },
	{ type: "strong", pattern: /^\*\*(?!\s)([\s\S]+?)(?<!\s)\*\*/ },
	{ type: "strong", pattern: /^__(?!\s)([\s\S]+?)(?<!\s)__(?![\p{L}\p{N}])/u },
	{ type: "strike", pattern: /^~~(?!\s)([\s\S]+?)(?<!\s)~~/ },
	{ type: "em", pattern: /^\*(?![\s*])([\s\S]+?)(?<![\s*])\*(?!\*)/ },
	{ type: "em", pattern: /^_(?![\s_])([\s\S]+?)(?<![\s_])_(?![\p{L}\p{N}])/u },
	{ type: "url", pattern: /^https?:\/\/[^\s<>]*[^\s<>.,:;"')\]!?*_~]/ },
];

// parseInline splits a run of markdown text into inline nodes.
export function parseInline(text) {
	const nodes = [];
	let plain = "";
	const flush = () => {
		if (plain) nodes.push({ type: "text", text: plain });
		plain = "";
	};
	let i = 0;
	while (i < text.length) {
		const rest = text.slice(i);
		const before = i > 0 ? text[i - 1] : "";
		let matched = null;
		for (const rule of inlineRules) {
			// Underscore emphasis and bare URLs start only at a word boundary.
			if (
				(rest[0] === "_" || rule.type === "url") &&
				/[\p{L}\p{N}]/u.test(before)
			)
				continue;
			const match = rest.match(rule.pattern);
			if (match) {
				matched = { rule, match };
				break;
			}
		}
		if (!matched) {
			plain += text[i];
			i++;
			continue;
		}
		const { rule, match } = matched;
		flush();
		switch (rule.type) {
			case "escape":
				plain = match[1];
				break;
			case "code":
				nodes.push({ type: "code", text: match[2].replace(/^ (.+) $/, "$1") });
				break;
			case "link":
				nodes.push({
					type: "link",
					href: safeHref(match[2]),
					children: parseInline(match[1]),
				});
				break;
			case "autolink":
				nodes.push({
					type: "link",
					href: safeHref(match[1]),
					children: [{ type: "text", text: match[1] }],
				});
				break;
			case "url":
				nodes.push({
					type: "link",
					href: safeHref(match[0]),
					children: [{ type: "text", text: match[0] }],
				});
				break;
			default:
				nodes.push({ type: rule.type, children: parseInline(match[1]) });
		}
		i += match[0].length;
	}
	flush();
	return nodes;
}

function renderInline(nodes) {
	return nodes.map((node, index) => {
		const key = `${node.type}-${index}`;
		switch (node.type) {
			case "code":
				return <code key={key}>{node.text}</code>;
			case "strong":
				return <strong key={key}>{renderInline(node.children)}</strong>;
			case "em":
				return <em key={key}>{renderInline(node.children)}</em>;
			case "strike":
				return <del key={key}>{renderInline(node.children)}</del>;
			case "link":
				if (!node.href)
					return <span key={key}>{renderInline(node.children)}</span>;
				return (
					<a key={key} href={node.href} target="_blank" rel="noreferrer">
						{renderInline(node.children)}
					</a>
				);
			default:
				return renderText(node.text, key);
		}
	});
}

// renderText keeps a paragraph's line breaks, the way GitHub shows comments.
function renderText(text, key) {
	const lines = text.split("\n");
	if (lines.length === 1) return text;
	return (
		<span key={key}>
			{lines.map((line, index) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: lines have no identity of their own.
				<span key={index}>
					{index > 0 && <br />}
					{line}
				</span>
			))}
		</span>
	);
}

function renderBlocks(blocks) {
	return blocks.map((block, index) => {
		const key = `${block.type}-${index}`;
		switch (block.type) {
			case "code":
				return (
					<pre key={key}>
						<code>{block.text}</code>
					</pre>
				);
			case "heading": {
				const Heading = `h${block.level}`;
				return (
					<Heading key={key}>{renderInline(parseInline(block.text))}</Heading>
				);
			}
			case "rule":
				return <hr key={key} />;
			case "quote":
				return <blockquote key={key}>{renderBlocks(block.blocks)}</blockquote>;
			case "list": {
				const items = block.items.map((item, itemIndex) => (
					// biome-ignore lint/suspicious/noArrayIndexKey: list items have no identity of their own.
					<li key={itemIndex}>{renderItem(item)}</li>
				));
				return block.ordered ? (
					<ol key={key} start={block.start === 1 ? undefined : block.start}>
						{items}
					</ol>
				) : (
					<ul key={key}>{items}</ul>
				);
			}
			case "table":
				return (
					<table key={key}>
						<thead>
							<tr>
								{block.header.map((cell, cellIndex) => (
									// biome-ignore lint/suspicious/noArrayIndexKey: cells have no identity of their own.
									<th key={cellIndex}>{renderInline(parseInline(cell))}</th>
								))}
							</tr>
						</thead>
						<tbody>
							{block.rows.map((row, rowIndex) => (
								// biome-ignore lint/suspicious/noArrayIndexKey: rows have no identity of their own.
								<tr key={rowIndex}>
									{row.map((cell, cellIndex) => (
										// biome-ignore lint/suspicious/noArrayIndexKey: cells have no identity of their own.
										<td key={cellIndex}>{renderInline(parseInline(cell))}</td>
									))}
								</tr>
							))}
						</tbody>
					</table>
				);
			default:
				return <p key={key}>{renderInline(parseInline(block.text))}</p>;
		}
	});
}

// A list item that is one paragraph is drawn without the paragraph's margins.
function renderItem(blocks) {
	if (blocks.length >= 1 && blocks[0].type === "paragraph") {
		return [
			<span key="text">{renderInline(parseInline(blocks[0].text))}</span>,
			...renderBlocks(blocks.slice(1)),
		];
	}
	return renderBlocks(blocks);
}

// Markdown draws text as rendered markdown.
export function Markdown({ text, className = "" }) {
	return (
		<div className={className ? `${className} markdown` : "markdown"}>
			{renderBlocks(parseBlocks(text || ""))}
		</div>
	);
}
