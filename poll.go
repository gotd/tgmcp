package main

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

// Poll is a poll attached to a message.
type Poll struct {
	Question       string       `json:"question" jsonschema:"poll question"`
	Options        []PollOption `json:"options" jsonschema:"answer options in display order"`
	TotalVoters    int          `json:"total_voters" jsonschema:"number of accounts that voted"`
	Closed         bool         `json:"closed,omitempty" jsonschema:"true if the poll no longer accepts votes"`
	MultipleChoice bool         `json:"multiple_choice,omitempty" jsonschema:"true if several options may be picked"`
	Quiz           bool         `json:"quiz,omitempty" jsonschema:"true if the poll is a quiz with one correct answer"`
	PublicVoters   bool         `json:"public_voters,omitempty" jsonschema:"true if voters are visible to everyone"`
}

// PollOption is a single answer option and its tally.
type PollOption struct {
	Index   int    `json:"index" jsonschema:"0-based index, as passed to vote_poll"`
	Text    string `json:"text" jsonschema:"option text"`
	Voters  int    `json:"voters,omitempty" jsonschema:"votes for this option, when results are visible"`
	Chosen  bool   `json:"chosen,omitempty" jsonschema:"true if the signed-in account picked this option"`
	Correct bool   `json:"correct,omitempty" jsonschema:"true if this is the correct quiz answer, once revealed"`
}

type sendPollInput struct {
	Chat             string   `json:"chat" jsonschema:"chat target"`
	Question         string   `json:"question" jsonschema:"poll question"`
	Options          []string `json:"options" jsonschema:"answer options, at least 2"`
	MultipleChoice   bool     `json:"multiple_choice,omitempty" jsonschema:"allow picking several options"`
	Quiz             bool     `json:"quiz,omitempty" jsonschema:"quiz mode; requires correct_option"`
	CorrectOption    *int     `json:"correct_option,omitempty" jsonschema:"0-based index of the correct option, for quizzes"`
	Explanation      string   `json:"explanation,omitempty" jsonschema:"text shown after answering a quiz"`
	PublicVoters     bool     `json:"public_voters,omitempty" jsonschema:"make votes visible to everyone"`
	ClosePeriod      int      `json:"close_period,omitempty" jsonschema:"seconds until the poll closes automatically"`
	ReplyToMessageID int      `json:"reply_to_message_id,omitempty" jsonschema:"reply to this message id"`
	Silent           bool     `json:"silent,omitempty" jsonschema:"send without notification"`
}

type sendPollOutput struct {
	OK        bool `json:"ok" jsonschema:"true on success"`
	MessageID int  `json:"message_id,omitempty" jsonschema:"sent message id if known"`
}

type votePollInput struct {
	Chat      string `json:"chat" jsonschema:"chat target"`
	MessageID int    `json:"message_id" jsonschema:"id of the message holding the poll"`
	Options   []int  `json:"options" jsonschema:"0-based option indices to vote for; empty retracts the vote"`
}

type votePollOutput struct {
	OK   bool  `json:"ok" jsonschema:"true on success"`
	Poll *Poll `json:"poll,omitempty" jsonschema:"the poll after voting"`
}

func (s *server) handleSendPoll(ctx context.Context, _ *mcp.CallToolRequest, in sendPollInput) (*mcp.CallToolResult, sendPollOutput, error) {
	if in.Chat == "" || in.Question == "" {
		return nil, sendPollOutput{}, errors.New("chat and question are required")
	}
	if len(in.Options) < 2 {
		return nil, sendPollOutput{}, errors.New("at least 2 options are required")
	}
	if in.Quiz && in.CorrectOption == nil {
		return nil, sendPollOutput{}, errors.New("quiz requires correct_option")
	}
	if in.CorrectOption != nil && (*in.CorrectOption < 0 || *in.CorrectOption >= len(in.Options)) {
		return nil, sendPollOutput{}, errors.Errorf("correct_option %d is out of range for %d options",
			*in.CorrectOption, len(in.Options))
	}

	answers := make([]message.PollAnswerOption, 0, len(in.Options))
	for i, text := range in.Options {
		if in.CorrectOption != nil && i == *in.CorrectOption {
			answers = append(answers, message.CorrectPollAnswer(text))

			continue
		}
		answers = append(answers, message.PollAnswer(text))
	}

	poll := message.Poll(in.Question, answers[0], answers[1], answers[2:]...)
	if in.MultipleChoice {
		poll = poll.MultipleChoice(true)
	}
	if in.PublicVoters {
		poll = poll.PublicVoters(true)
	}
	if in.ClosePeriod > 0 {
		poll = poll.ClosePeriod(time.Duration(in.ClosePeriod) * time.Second)
	}
	if in.Explanation != "" {
		poll = poll.Explanation(in.Explanation)
	}

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, sendPollOutput{}, err
	}

	b := message.NewSender(s.api).To(p)
	if in.Silent {
		b.Silent()
	}
	if in.ReplyToMessageID > 0 {
		b.Reply(in.ReplyToMessageID)
	}

	upd, err := b.Media(ctx, poll)
	if err != nil {
		return nil, sendPollOutput{}, errors.Wrap(err, "send poll")
	}

	return nil, sendPollOutput{OK: true, MessageID: extractSentMessageID(upd)}, nil
}

func (s *server) handleVotePoll(ctx context.Context, _ *mcp.CallToolRequest, in votePollInput) (*mcp.CallToolResult, votePollOutput, error) {
	if in.Chat == "" || in.MessageID <= 0 {
		return nil, votePollOutput{}, errors.New("chat and message_id are required")
	}

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, votePollOutput{}, err
	}

	msg, err := fetchMessage(ctx, s.api, p, in.MessageID)
	if err != nil {
		return nil, votePollOutput{}, err
	}
	media, ok := msg.Media.(*tg.MessageMediaPoll)
	if !ok {
		return nil, votePollOutput{}, errors.Errorf("message %d has no poll", in.MessageID)
	}

	// Option bytes are chosen by whoever created the poll, so they cannot be
	// derived from the index: map through the answer list instead.
	options := make([][]byte, 0, len(in.Options))
	for _, idx := range in.Options {
		if idx < 0 || idx >= len(media.Poll.Answers) {
			return nil, votePollOutput{}, errors.Errorf("option %d is out of range for %d options",
				idx, len(media.Poll.Answers))
		}
		answer, ok := media.Poll.Answers[idx].(*tg.PollAnswer)
		if !ok {
			return nil, votePollOutput{}, errors.Errorf("option %d is not a poll answer", idx)
		}
		options = append(options, answer.Option)
	}

	req := &tg.MessagesSendVoteRequest{Peer: p, MsgID: in.MessageID, Options: options}
	if _, err := s.api.MessagesSendVote(ctx, req); err != nil {
		return nil, votePollOutput{}, errors.Wrap(err, "send vote")
	}

	// Re-read so the returned tally reflects the vote just cast.
	voted, err := fetchMessage(ctx, s.api, p, in.MessageID)
	if err != nil {
		return nil, votePollOutput{OK: true}, nil
	}
	if m, ok := voted.Media.(*tg.MessageMediaPoll); ok {
		return nil, votePollOutput{OK: true, Poll: pollFromMedia(m)}, nil
	}

	return nil, votePollOutput{OK: true}, nil
}

// pollFromMedia converts poll media into the shape returned to MCP clients.
func pollFromMedia(m *tg.MessageMediaPoll) *Poll {
	out := &Poll{
		Question:       m.Poll.Question.Text,
		TotalVoters:    m.Results.TotalVoters,
		Closed:         m.Poll.Closed,
		MultipleChoice: m.Poll.MultipleChoice,
		Quiz:           m.Poll.Quiz,
		PublicVoters:   m.Poll.PublicVoters,
	}

	// Tallies are keyed by option bytes rather than position, and are absent
	// entirely until results are visible.
	tally := make(map[string]tg.PollAnswerVoters, len(m.Results.Results))
	for _, r := range m.Results.Results {
		tally[string(r.Option)] = r
	}

	for i, a := range m.Poll.Answers {
		answer, ok := a.(*tg.PollAnswer)
		if !ok {
			continue
		}
		option := PollOption{Index: i, Text: answer.Text.Text}
		if v, ok := tally[string(answer.Option)]; ok {
			option.Voters = v.Voters
			option.Chosen = v.Chosen
			option.Correct = v.Correct
		}
		out.Options = append(out.Options, option)
	}

	return out
}
