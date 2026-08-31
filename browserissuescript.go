package main

// browserIssueRowsScript is the single expression evaluated in the tab per page
// load. It returns the rows it could read, whether it recognised the page at
// all, and the pager's next target.
//
// Extraction is anchored on the issue-row link href (/OWNER/REPO/issues/N),
// which is the one part of GitHub's issues page that cannot change without the
// URLs changing with it. Everything else is read from within the row that link
// belongs to, and every selector that names a class or a test id is written as
// one alternative among several: GitHub renames those freely, and a single
// stale class name must not be able to blind the extractor. Anchors carrying a
// fragment are skipped so a row's comment-count link cannot be mistaken for its
// title link.
const browserIssueRowsScript = `(function () {
  var rowPattern = /^(?:https:\/\/github\.com)?\/([^\/\s]+)\/([^\/\s]+)\/issues\/(\d+)(?:\?[^#]*)?$/;
  var list =
    document.querySelector('[data-listview-component="items-list"]') ||
    document.querySelector('[data-testid="issue-list"]') ||
    document.querySelector('section[aria-label*="issue" i]') ||
    document.querySelector('.js-navigation-container');
  var container = list || document;

  var rows = [];
  var seen = {};
  var anchors = container.querySelectorAll('a[href*="/issues/"]');
  for (var i = 0; i < anchors.length; i++) {
    var anchor = anchors[i];
    var match = rowPattern.exec(anchor.getAttribute('href') || '');
    if (!match) continue;
    var repository = match[1] + '/' + match[2];
    var key = repository + '#' + match[3];
    if (seen[key]) continue;
    var title = (anchor.textContent || '').trim();
    if (!title) continue;
    seen[key] = true;

    var row =
      anchor.closest('[data-testid="issue-list-item"]') ||
      anchor.closest('[data-testid="list-view-item"]') ||
      anchor.closest('[role="listitem"]') ||
      anchor.closest('li') ||
      anchor.closest('tr') ||
      anchor.parentElement ||
      anchor;

    var labels = [];
    var chips = row.querySelectorAll('[class*="IssueLabelToken"], [class*="prc-Token-IssueLabel"], a[href*="/labels/"], [data-testid="issue-label"], .IssueLabel');
    for (var j = 0; j < chips.length; j++) {
      var name = (chips[j].getAttribute('data-name') || chips[j].textContent || '').trim();
      if (name && labels.indexOf(name) < 0) labels.push(name);
    }

    var closed = !!row.querySelector('svg.octicon-issue-closed, [data-testid="issue-closed-icon"]');
    if (!closed) {
      var labelled = row.querySelectorAll('[aria-label]');
      for (var k = 0; k < labelled.length && !closed; k++) {
        closed = /\bclosed\b/i.test(labelled[k].getAttribute('aria-label') || '');
      }
    }

    rows.push({
      number: parseInt(match[3], 10),
      repository: repository,
      title: title,
      state: closed ? 'closed' : 'open',
      labels: labels
    });
  }

  var text = document.body ? document.body.innerText || '' : '';
  // A list container holding no rows is an empty list whether or not the
  // blankslate GitHub renders beside it is recognised: reporting it as markup
  // the extractor could not read turned a repository with no ready issues into
  // a failure on every five-second tick (issue #413). The fallback container is
  // the document itself, which says nothing about a list, so only a container
  // the page actually named counts as that evidence.
  var empty = !!(
    document.querySelector('[data-testid="issue-list-empty-state"]') ||
    document.querySelector('[data-testid="list-view-no-results"]') ||
    document.querySelector('.blankslate') ||
    /no results matched your search|there aren't any (?:open )?issues|no open issues/i.test(text)
  );

  var pager =
    document.querySelector('a[rel="next"]') ||
    document.querySelector('a[data-testid="pagination-next"]') ||
    document.querySelector('a[aria-label="Next Page"]') ||
    document.querySelector('a[aria-label="Next"]');
  var next = pager && pager.getAttribute('aria-disabled') !== 'true' ? pager.href : '';

  // container reports whether the page named a list at all. A named list that
  // drew no rows is an empty list rather than markup the extractor could not
  // read, but only once the caller's render wait is over, so the decision is
  // left to it (issue #413). The fallback container is the document itself,
  // which says nothing about a list, so it does not count.
  return { rows: rows, recognized: rows.length > 0 || empty, empty: empty, container: !!list, next: next };
})()`
