package core

import (
	"context"
	"time"
)

// Comment is a single issue or pull request comment relevant to the
// cooperative handoff protocol.
type Comment struct {
	Body      string
	Author    string
	CreatedAt time.Time
}

// CommentPoster posts a comment to an issue or pull request.
type CommentPoster interface {
	PostComment(ctx context.Context, repo string, number int, body string) error
}

// CommentLister lists the comments on an issue or pull request.
type CommentLister interface {
	ListComments(ctx context.Context, repo string, number int) ([]Comment, error)
}

// CommentClient is the combined capability needed to run the handoff
// handshake described in issue #214.
type CommentClient interface {
	CommentPoster
	CommentLister
}

// CommentReactor adds an emoji reaction to a specific comment, identified by
// its GitHub comment ID. It is kept separate from CommentClient so a driver
// with no reaction affordance (a fake in a test, or a future comment source
// with no API to react through) is not forced to implement it (issue #581).
type CommentReactor interface {
	AddReaction(ctx context.Context, repo string, commentID int64, content string) error
}
