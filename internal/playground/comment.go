package playground

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/koblas/exemplar/internal/config"
)

// Comment is an issue_comment on an exemplar-config PR.
type Comment struct {
	Repo   string // the repo the comment is in
	PR     int
	Author string
	// AuthorType is GitHub's user type ("User", "Bot"); bots are ignored,
	// which also keeps the handler from answering its own replies.
	AuthorType string
	Body       string
}

// ConfigPR reads a config PR: its base and head trees and changed paths.
type ConfigPR interface {
	Load(ctx context.Context, pr int) (base, head fs.FS, changed []string, headSHA string, err error)
}

// Replier answers on a config PR.
type Replier interface {
	Comment(ctx context.Context, pr int, body string) error
}

// Handler answers /exemplar try comments on exemplar-config PRs.
type Handler struct {
	ConfigRepo string
	Config     ConfigPR
	Runner     *Runner
	Replier    Replier
	Logger     *slog.Logger
}

// Handle runs a /exemplar try in c, if there is one, and replies. handled
// is false when the comment is not a command for this handler.
func (h *Handler) Handle(ctx context.Context, c Comment) (bool, error) {
	if !strings.EqualFold(c.Repo, h.ConfigRepo) || c.AuthorType == "Bot" {
		return false, nil
	}
	target, found, perr := ParseTry(c.Body)
	if !found {
		return false, nil
	}
	reply := func(body string) (bool, error) {
		return true, h.Replier.Comment(ctx, c.PR, fmt.Sprintf("@%s %s", c.Author, body))
	}
	if perr != nil {
		return reply(perr.Error())
	}
	base, head, changed, headSHA, err := h.Config.Load(ctx, c.PR)
	if err != nil {
		h.log().Error("playground: load config PR", "pr", c.PR, "err", err)
		return reply("could not read this PR's rules; nothing ran.")
	}
	snap, ids, err := DraftSnapshot(base, head, changed, headSHA)
	var probs config.Problems
	switch {
	case errors.Is(err, ErrNoDraftRules):
		return reply("this PR changes no rule files, so there is nothing to try.")
	case errors.As(err, &probs):
		return reply("the draft does not validate, so nothing ran:\n\n```\n" + probs.Error() + "\n```")
	case err != nil:
		return reply(err.Error())
	}
	out, err := h.Runner.Try(ctx, Request{Actor: c.Author, Snapshot: snap, RuleIDs: ids, Target: target})
	if err != nil {
		h.log().Error("playground: try", "pr", c.PR, "target", target.String(), "err", err)
		return reply(fmt.Sprintf("the try on %s failed; nothing was posted there.", target))
	}
	h.log().Info("playground: try", "pr", c.PR, "target", target.String(), "actor", c.Author,
		"ran", out.Ran, "denied", out.Denied, "findings", len(out.Findings), "cost_usd", out.CostUSD)
	return reply(out.Reply)
}

func (h *Handler) log() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}
