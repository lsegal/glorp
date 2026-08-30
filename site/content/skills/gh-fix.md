---
title: gh-fix
description: "gh-fix takes ownership of a GitHub issue and drives the whole delivery loop: code, proof, CI, merge, and the loose ends after it."
ogimage: images/og-gh-fix.png
---

<style>
.fix-hero { max-width: none; }
.draft-section { padding-top: 3rem; }
.draft-pill { display: inline-block; padding: .2rem .55rem; border: 1px solid #d0d7de; border-radius: 2rem; color: #57606a; background: #f6f8fa; font-size: .7rem; font-weight: 800; }
.commit-list { display: grid; gap: .65rem; margin: 1.2rem 0 0; padding: 0; list-style: none; }.commit-list li { display: grid; grid-template-columns: 1fr auto; gap: .35rem .7rem; padding-top: .65rem; border-top: 1px solid #d8dee4; font-size: .78rem; }.commit-list small { color: #57606a; font-size: .68rem; }
.proof-section { display: grid; grid-template-columns: .95fr 1.05fr; align-items: center; gap: 5rem; padding: 5rem 0 3rem; }.proof-section h2 { margin: .25rem 0 1.15rem; font-size: clamp(2.1rem, 5vw, 3.7rem); }.proof-section > div > p { color: var(--muted); }.proof-copy { display: grid; gap: .25rem; margin-top: 1.25rem; padding: 1rem; border-left: 3px solid var(--orange); background: rgb(244 241 232 / 68%); }.proof-copy span { color: var(--muted); font-size: .9rem; }.proof-shot { margin: 0 auto; max-width: 320px; overflow: hidden; border: 1px solid var(--line); border-radius: .8rem; background: #fff; box-shadow: 15px 17px 0 rgb(36 36 92 / 12%); }.proof-shot img { display: block; width: 100%; height: auto; }.proof-shot figcaption { padding: .65rem .9rem; color: #57606a; border-top: 1px solid #d8dee4; background: white; font-size: .95rem; }
@media (max-width: 640px) { .proof-section { grid-template-columns: 1fr; gap: 2rem; padding: 3.5rem 0; }.fix-hero h1 { font-size: clamp(3.2rem, 16vw, 5.5rem); } }
</style>

<section class="skill-hero fix-hero">
  <p class="eyebrow">GitHub issue closer</p>
  <h1>From open issue<br>to <em>merged.</em></h1>
  <p class="hero-copy"><code>gh-fix</code> takes ownership of a GitHub issue and drives the whole delivery loop: code, proof, CI, merge, and the loose ends after it.</p>
  <div class="hero-actions"><a class="button primary" href="#install">Install gh-fix</a><a class="button ghost" href="https://github.com/lsegal/glorp/tree/main/.agents/skills/gh-fix">Read the skill</a></div>
</section>

<section class="process-strip" aria-label="gh-fix process">
  <div><b>01</b><span>Read the full issue<br>and its context</span></div><div><b>02</b><span>Open a draft PR<br>in an isolated clone</span></div><div><b>03</b><span>Build, test, show<br>the finished UI</span></div><div><b>04</b><span>Stabilize CI, merge,<br>close follow-ups</span></div>
</section>

<section class="split-section">
  <div><p class="eyebrow">The promise</p><h2>It carries the ticket across the finish line.</h2><p>Give it an issue number. It checks the request and discussion history, works in a fresh clone, and makes progress visible immediately with a linked draft pull request.</p><p>Then it adds focused tests and a changelog note, captures proof for UI work, follows every required CI check, resolves what its change breaks, and merges only when GitHub agrees.</p></div>
  <div class="github-mock issue-mock" aria-label="Sample GitHub issue card"><div class="mock-top"><span class="gh-mark">&#9673;</span><span>lsegal / glorp</span><span class="mock-dots">&bull;&bull;&bull;</span></div><div class="mock-body"><span class="open-pill">Open</span><h3>Add Sign Out button to Settings page</h3><p>Issue opened by you &middot; assigned to you</p><div class="mock-comment"><b>gh-fix</b><span>I found the path, opened a draft, and am validating the button.</span></div></div></div>
</section>

<section class="split-section draft-section"><div class="github-mock draft-mock" aria-label="Sample draft pull request with checkpoints"><div class="mock-top"><span class="gh-mark">&#9673;</span><span>Pull request</span><span class="mock-dots">&bull;&bull;&bull;</span></div><div class="mock-body"><span class="draft-pill">Draft</span><h3>Add Sign Out button to Settings page <span>#309</span></h3><p>Open while the work is still in progress</p><ol class="commit-list"><li><code>Start work on issue #308</code><small>draft opened</small></li><li><code>Checkpoint issue #308 progress</code><small>safe to resume</small></li><li><code>Add Sign Out button to Settings page</code><small>ready for review</small></li></ol></div></div><div><p class="eyebrow">Draft means protected progress</p><h2>Open early. Keep the work visible.</h2><p>A draft pull request is the workbench, not a review request. It links the issue, records the branch, and gives everyone a live place to see what is underway.</p><p>As work changes, <code>gh-fix</code> pushes checkpoint commits at least every five minutes. If the session stops, the next agent resumes from the same branch instead of recreating the work.</p></div></section>

<section class="workflow-section"><p class="eyebrow">Nothing falls through</p><h2>A fully unbroken build loop.</h2><div class="workflow-loop">
  <svg class="loop-svg" viewBox="0 0 680 680" aria-hidden="true">
    <defs>
      <marker id="loop-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="var(--orange)" /></marker>
      <marker id="loop-arrow-spin" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="#8250df" /></marker>
    </defs>
    <path d="M 340,200 A 140,140 0 0,1 461.24,270" class="loop-line" marker-end="url(#loop-arrow)" />
    <path d="M 461.24,270 A 140,140 0 0,1 461.24,410" class="loop-line" marker-end="url(#loop-arrow)" />
    <path d="M 461.24,410 A 140,140 0 0,1 340,480" class="loop-line" marker-end="url(#loop-arrow)" />
    <path d="M 340,480 A 140,140 0 0,1 218.76,410" class="loop-line" marker-end="url(#loop-arrow)" />
    <path d="M 218.76,410 A 140,140 0 0,1 218.76,270" class="loop-line" marker-end="url(#loop-arrow)" />
    <path d="M 218.76,270 A 140,140 0 0,1 340,200" class="loop-line loop-line-spin" marker-end="url(#loop-arrow-spin)" />
  </svg>
  <div class="node" style="left:50%;top:13.24%"><i class="step-num">01</i><b>Issue</b><span>Request and context</span></div>
  <div class="node" style="left:86%;top:31.62%"><i class="step-num">02</i><b>Draft PR</b><span>Branch and checkpoints</span></div>
  <div class="node accent" style="left:86%;top:68.38%"><i class="step-num">03</i><b>Proof</b><span>Tests and screenshots</span></div>
  <div class="node" style="left:50%;top:86.76%"><i class="step-num">04</i><b>Green CI</b><span>Reruns until clean</span></div>
  <div class="node done" style="left:14%;top:68.38%"><i class="step-num">05</i><b>Merged</b><span>Issue closed</span></div>
  <div class="node spinoff" style="left:14%;top:31.62%"><i class="step-num">06</i><b>Follow-ups</b><span>Leftovers become new issues</span></div>
</div><div class="callout-grid"><article><strong>Context stays in scope</strong><p>It reads the issue, comments, linked work, and direct Glorp instructions before acting.</p></article><article><strong>UI changes get shown</strong><p>Browser work earns representative screenshots or a recording in the pull request.</p></article><article><strong>Nothing gets stranded</strong><p>Actionable leftovers become linked follow-up issues, routed back into the team&rsquo;s flow.</p></article></div></section>

<section class="split-section reverse"><div class="github-mock pr-mock" aria-label="Sample GitHub pull request card"><div class="mock-top"><span class="gh-mark">&#9673;</span><span>Pull request</span><span class="mock-dots">&bull;&bull;&bull;</span></div><div class="mock-body"><span class="merged-pill">Merged</span><h3>Add Sign Out button to Settings page</h3><p>3 commits &middot; 2 checks passed &middot; 1 screenshot</p><div class="check-row"><span>&check;</span> Build and test <b>Passed</b></div><div class="check-row"><span>&check;</span> Deploy Pages <b>Passed</b></div></div></div><div><p class="eyebrow">No drive-by patches</p><h2>It merges only after the checks pass.</h2><p>The skill watches the current commit&rsquo;s checks and reads failures precisely before making the smallest correct change. Transient failures get one automatic re-run; genuine blockers remain plainly documented on the pull request.</p></div></section>

<section class="proof-section"><div><p class="eyebrow">Proof people can inspect</p><h2>Show the changed state, then name what it proves.</h2><p>For browser work, the finished pull request includes a representative screenshot or recording. The caption says what changed and the proof text names the state, interaction, and check that made it trustworthy.</p><div class="proof-copy"><b>Proof attached</b><span>The Settings page now shows a working Sign Out button, matching the passed browser test.</span></div></div><figure class="proof-shot"><img src="{{< relurl \"images/gh-fix-screenshot-proof.png\" >}}" alt="Sample screenshot of a Sign Out button on a Settings page" /><figcaption>Proof: the Sign Out button now shows up on the Settings page.</figcaption></figure></section>

<section id="install" class="install-section"><p class="eyebrow">Install only what you need</p><h2>Bring <code>gh-fix</code> into your agent.</h2><div class="install-grid"><article><h3>skills.sh</h3><p>Install for your selected agents in one command.</p><pre><code>npx --yes skills add lsegal/glorp@gh-fix --global --agent codex --agent claude-code -y</code></pre></article><article><h3>GitHub CLI</h3><p>Install the bundled skill directly for your agent.</p><pre><code>gh skill install lsegal/glorp gh-fix --allow-hidden-dirs --agent codex --agent claude-code --scope user</code></pre></article></div><p class="run-example"><span>Run it</span><code>/gh-fix owner/repo#123</code></p></section>
