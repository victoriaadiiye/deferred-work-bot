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
