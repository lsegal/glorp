package main

// browserCommentsScript is the single expression evaluated in the tab per issue
// page load. It returns the comments it could read and whether it recognised
// the page as an issue or pull request conversation at all.
//
// Extraction is anchored on the comment permalink id (`issuecomment-<id>`),
// which is the one part of a conversation page that cannot change without the
// permalinks changing with it. Everything else is read from within the element
// that id belongs to, and every selector naming a class or a test id is written
// as one alternative among several, the same way the issues-list extractor is:
// GitHub renames those freely, and a single stale class name must not be able
// to blind the extractor.
//
// The handoff protocol's comments are plain one-line sentences ending in a
// `/glorp:ID` signature, so the rendered text of a comment body carries
// everything parseClaim and mentionedIdentity read. Nothing here tries to
// recover Markdown source.
const browserCommentsScript = `(function () {
  var idPattern = /^issuecomment-(\d+)$/;
  var nodes = document.querySelectorAll('[id^="issuecomment-"]');
  var comments = [];
  var seen = {};
  for (var i = 0; i < nodes.length; i++) {
    var node = nodes[i];
    var match = idPattern.exec(node.getAttribute('id') || '');
    if (!match || seen[match[1]]) continue;
    seen[match[1]] = true;

    var bodyNode =
      node.querySelector('[data-testid="comment-viewer-outer-box"] .markdown-body') ||
      node.querySelector('[data-testid="markdown-body"]') ||
      node.querySelector('[data-testid="comment-body"]') ||
      node.querySelector('.comment-body') ||
      node.querySelector('.markdown-body') ||
      node.querySelector('[class*="MarkdownViewer"]') ||
      node.querySelector('task-lists') ||
      node;
    var body = (bodyNode.innerText || bodyNode.textContent || '').trim();
    if (!body) continue;

    var author = '';
    var authorNode =
      node.querySelector('a[data-testid="avatar-link"]') ||
      node.querySelector('a[data-hovercard-type="user"]') ||
      node.querySelector('a.author') ||
      node.querySelector('[class*="author"] a[href^="/"]') ||
      node.querySelector('a[href^="/"][class*="Link"]');
    if (authorNode) {
      var href = authorNode.getAttribute('href') || '';
      var user = /^\/([^\/\s?#]+)(?:[?#].*)?$/.exec(href);
      author = user ? user[1] : (authorNode.textContent || '').trim();
    }

    var createdAt = '';
    var timeNode = node.querySelector('relative-time[datetime], time[datetime], [datetime]');
    if (timeNode) createdAt = (timeNode.getAttribute('datetime') || '').trim();

    comments.push({ id: match[1], author: author, body: body, createdAt: createdAt });
  }

  // A conversation with no comments has to be told apart from a page that has
  // not drawn yet, so the page is asked for a marker it only carries once the
  // conversation itself is rendered. Any comment found is its own evidence.
  var recognized = comments.length > 0 ||
    !!document.querySelector('[data-testid="issue-viewer-container"]') ||
    !!document.querySelector('[data-testid="issue-body"]') ||
    !!document.querySelector('[data-testid="issue-body-viewer"]') ||
    !!document.querySelector('#partial-discussion-header') ||
    !!document.querySelector('.js-discussion') ||
    !!document.querySelector('[data-testid="issue-header"]') ||
    !!document.querySelector('[data-testid="issue-title"]') ||
    !!document.querySelector('.gh-header-title');

  return { comments: comments, recognized: recognized };
})()`
