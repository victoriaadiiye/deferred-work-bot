package main

import (
	"sync"
	"testing"

	"github.com/slack-go/slack"
)

type fakeSlack struct {
	mu        sync.Mutex
	botID     string
	posted    []postedMsg
	reactions []reactRef
	replies   map[string][]slack.Message // keyed by ts
}

type postedMsg struct {
	Channel string
	TS      string
	Text    string
}

type reactRef struct {
	Action  string // add|remove
	Name    string
	Channel string
	TS      string
}

func newFakeSlack(botID string) *fakeSlack {
	return &fakeSlack{botID: botID, replies: map[string][]slack.Message{}}
}

func (f *fakeSlack) PostMessage(channelID string, options ...slack.MsgOption) (string, string, error) {
	// Compose the message to extract text (best-effort: just record the option count + channel).
	f.mu.Lock()
	defer f.mu.Unlock()
	ts := generateTS(len(f.posted))
	f.posted = append(f.posted, postedMsg{Channel: channelID, TS: ts, Text: optionsText(options)})
	return channelID, ts, nil
}

func (f *fakeSlack) AddReaction(name string, item slack.ItemRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, reactRef{Action: "add", Name: name, Channel: item.Channel, TS: item.Timestamp})
	return nil
}

func (f *fakeSlack) RemoveReaction(name string, item slack.ItemRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, reactRef{Action: "remove", Name: name, Channel: item.Channel, TS: item.Timestamp})
	return nil
}

func (f *fakeSlack) GetConversationReplies(params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	return f.replies[params.Timestamp], false, "", nil
}

func (f *fakeSlack) GetPermalink(p *slack.PermalinkParameters) (string, error) {
	return "https://slack.example/archives/" + p.Channel + "/p" + p.Ts, nil
}

func (f *fakeSlack) AuthTest() (*slack.AuthTestResponse, error) {
	return &slack.AuthTestResponse{UserID: f.botID}, nil
}

func generateTS(n int) string { return "1700000000.00010" + string(rune('0'+n%10)) }
func optionsText(_ []slack.MsgOption) string { return "" }

func TestFakeSlack_PostAndReact(t *testing.T) {
	f := newFakeSlack("UBOT")
	_, ts, _ := f.PostMessage("C1", slack.MsgOptionText("hi", false))
	if ts == "" {
		t.Fatal("expected ts")
	}
	f.AddReaction("eyes", slack.ItemRef{Channel: "C1", Timestamp: ts})
	if len(f.reactions) != 1 || f.reactions[0].Name != "eyes" {
		t.Fatalf("reactions: %+v", f.reactions)
	}
}

func TestRouter_NewItemInWatchedChannel(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels:   map[string]bool{"C1": true},
		ApprovalThreshold: 3,
	}
	r.HandleMessage(MessageEvent{
		Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "park this for later",
	})
	it, err := store.GetItemByTS("C1", "1700.1")
	if err != nil {
		t.Fatalf("item not stored: %v", err)
	}
	if it.Status != "collecting" {
		t.Fatalf("status: %s", it.Status)
	}
	if len(fake.reactions) != 1 || fake.reactions[0].Name != "eyes" {
		t.Fatalf("expected :eyes: reaction, got %+v", fake.reactions)
	}
}

func TestRouter_IgnoresOwnMessages(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", User: "UBOT", Text: "I am the bot"})
	_, err := store.GetItemByTS("C1", "1700.2")
	if err != ErrNotFound {
		t.Fatal("bot messages should not be tracked")
	}
}

func TestRouter_IgnoresThreadReplyAsItem(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.3", ThreadTS: "1700.1", User: "U1", Text: "reply"})
	_, err := store.GetItemByTS("C1", "1700.3")
	if err != ErrNotFound {
		t.Fatal("thread replies are not new items")
	}
}

func TestRouter_NonWatchedChannelIgnored(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}}
	r.HandleMessage(MessageEvent{Channel: "C999", TS: "1700.4", User: "U1", Text: "park this"})
	_, err := store.GetItemByTS("C999", "1700.4")
	if err != ErrNotFound {
		t.Fatal("non-watched channels should be ignored for top-level posts")
	}
}

func TestRouter_ReactionAddedCountsAsVote(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 1 {
		t.Fatalf("expected 1 vote, got %d", n)
	}
}

func TestRouter_AuthorReactionExcluded(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U_AUTHOR", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected author vote excluded, got %d", n)
	}
}

func TestRouter_BotReactionExcluded(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "UBOT", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected bot vote excluded, got %d", n)
	}
}

func TestRouter_ReactionRemovedDecrements(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	r.HandleReactionRemoved(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected 0 votes after removal, got %d", n)
	}
}

func TestRouter_CancelReactionMarksCancelled(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{CancelReactions: []string{"x"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", it.Status)
	}
}

func TestRouter_ReplyApproveKeywordCountsVote(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReplies: []string{"lgtm", "+1", "approve"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "LGTM"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 1 {
		t.Fatalf("expected 1 vote, got %d", n)
	}
}

func TestRouter_ReplyCancelKeyword(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{CancelReplies: []string{"cancel"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> cancel"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", it.Status)
	}
}

func TestRouter_BotMentionDispatch(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> status"})
	// Bot should have posted at least one reply.
	if len(fake.posted) < 1 {
		t.Fatal("expected bot to post status reply")
	}
}

func TestRouter_AppMentionInNonWatchedChannelCreatesItem(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleAppMention(MessageEvent{Channel: "C999", TS: "1700.5", User: "U2", Text: "<@UBOT> track this work"})
	if _, err := store.GetItemByTS("C999", "1700.5"); err != nil {
		t.Fatalf("expected item, got: %v", err)
	}
}

func TestRouter_AppMentionInThreadDispatchesCommand(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleAppMention(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> status"})
	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
}
