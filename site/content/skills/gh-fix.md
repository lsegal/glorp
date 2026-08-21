---
title: gh-fix
---

<section class="skill-hero fix-hero">
  <p class="eyebrow">GitHub issue closer</p>
  <h1>From “it’s broken”<br>to <em>merged.</em></h1>
  <p class="hero-copy"><code>gh-fix</code> takes ownership of a GitHub issue and drives the whole repair loop: code, proof, CI, merge, and the loose ends after it.</p>
  <div class="hero-actions"><a class="button primary" href="#install">Install gh-fix</a><a class="button ghost" href="https://github.com/lsegal/glorp/tree/main/.agents/skills/gh-fix">Read the skill</a></div>
</section>

<section class="process-strip" aria-label="gh-fix process">
  <div><b>01</b><span>Read the full issue<br>and its context</span></div><div><b>02</b><span>Open a draft PR<br>in an isolated clone</span></div><div><b>03</b><span>Fix, test, show<br>the finished UI</span></div><div><b>04</b><span>Repair CI, merge,<br>close follow-ups</span></div>
</section>

<section class="split-section">
  <div><p class="eyebrow">The promise</p><h2>It carries the ticket across the finish line.</h2><p>Give it an issue number. It checks the request and discussion history, works in a fresh clone, and makes progress visible immediately with a linked draft pull request.</p><p>Then it adds focused tests and a changelog note, captures proof for UI work, follows every required CI check, repairs what its change breaks, and merges only when GitHub agrees.</p></div>
  <div class="github-mock issue-mock" aria-label="Illustrative GitHub issue card"><div class="mock-top"><span class="gh-mark">◉</span><span>lsegal / glorp</span><span class="mock-dots">•••</span></div><div class="mock-body"><span class="open-pill">Open</span><h3>Fix the status card after a retry</h3><p>Issue opened by you · agent-ready</p><div class="mock-comment"><b>gh-fix</b><span>I found the path, opened a draft, and am validating the repair.</span></div></div></div>
</section>

<section class="workflow-section"><p class="eyebrow">A visible loop</p><h2>Every handoff has a receipt.</h2><div class="workflow-map fix-map"><div class="node"><b>Issue</b><span>request + context</span></div><i>→</i><div class="node"><b>Draft PR</b><span>branch + checkpoints</span></div><i>→</i><div class="node accent"><b>Proof</b><span>tests + screenshots</span></div><i>→</i><div class="node"><b>Green CI</b><span>repair until clean</span></div><i>→</i><div class="node done"><b>Merged</b><span>issue closed</span></div></div><div class="callout-grid"><article><strong>Context stays in scope</strong><p>It reads the issue, comments, linked work, and direct Glorp instructions before acting.</p></article><article><strong>UI changes get shown</strong><p>Browser work earns representative screenshots or a recording in the pull request.</p></article><article><strong>Nothing gets stranded</strong><p>Actionable leftovers become linked follow-up issues, routed back into the team’s flow.</p></article></div></section>

<section class="split-section reverse"><div class="github-mock pr-mock" aria-label="Illustrative GitHub pull request card"><div class="mock-top"><span class="gh-mark">◉</span><span>Pull request</span><span class="mock-dots">•••</span></div><div class="mock-body"><span class="merged-pill">Merged</span><h3>Fix retry status visibility</h3><p>3 commits · 2 checks passed · 1 screenshot</p><div class="check-row"><span>✓</span> Build and test <b>Passed</b></div><div class="check-row"><span>✓</span> Deploy Pages <b>Passed</b></div></div></div><div><p class="eyebrow">No drive-by patches</p><h2>It waits for the checks—not the other way around.</h2><p>The skill watches the current commit’s checks, reads failures precisely, and makes the smallest correct repair. Transient failures get one retry; genuine blockers remain plainly documented on the pull request.</p></div></section>

<section id="install" class="install-section"><p class="eyebrow">Install only what you need</p><h2>Bring <code>gh-fix</code> into your agent.</h2><div class="install-grid"><article><h3>skills.sh</h3><p>Install for Codex and Claude Code in one command.</p><pre><code>npx --yes skills add lsegal/glorp@gh-fix --global --agent codex --agent claude-code -y</code></pre></article><article><h3>GitHub CLI</h3><p>Clone it, then copy the skill into Codex’s skills folder.</p><pre><code>gh repo clone lsegal/glorp
cp -R glorp/.agents/skills/gh-fix ~/.codex/skills/</code></pre></article></div><p class="run-example"><span>Run it</span><code>/gh-fix lsegal/glorp#304</code></p></section>
