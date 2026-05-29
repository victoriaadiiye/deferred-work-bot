package main

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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

func optionsText(options []slack.MsgOption) string {
	_, vals, err := slack.UnsafeApplyMsgOptions("", "", "https://slack.com/api/", options...)
	if err != nil {
		return ""
	}
	return vals.Get("text")
}

var _ url.Values // ensure net/url is used

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

func TestRouter_MessageDeletedCancels(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessageDeleted(MessageEvent{Channel: "C1", TS: "1700.1"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", it.Status)
	}
}

func TestRouter_MessageEditedUpdatesText(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "original"})
	r.HandleMessageChanged(MessageEvent{Channel: "C1", TS: "1700.1", Text: "edited text", User: "U_AUTHOR"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Text != "edited text" {
		t.Fatalf("text not updated: %s", it.Text)
	}
}

func TestRouter_ResolutionNewKeyword(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	p := &Proposal{ItemID: it.ID, SlackTS: "1700.2", DraftJSON: `{"summary":"s"}`, RelatedTicketsJSON: "[]", Branch: "awaiting_resolution", ExistingTicketKey: "QORK-5", Status: "awaiting_resolution"}
	store.InsertProposal(p)
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.3", ThreadTS: "1700.1", User: "U2", Text: "let's file as new"})
	got, _ := store.GetLatestProposal(it.ID)
	if got.Branch != "new" {
		t.Fatalf("branch: %s", got.Branch)
	}
}

func TestCmdStatus_ReportsCounts(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U3", Text: "<@UBOT> status"})
	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
}

func TestCmdFileNow_TransitionsToProposing(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 1)}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3, Worker: w}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> file now"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "proposing" {
		t.Fatalf("status: %s", it.Status)
	}
	select {
	case <-w.queue:
	default:
		t.Fatal("expected ProposeJob enqueued")
	}
}

func TestCmdProject_UpdatesSubproject(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> project: qatalyst"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Subproject != "qatalyst" {
		t.Fatalf("subproject: %s", it.Subproject)
	}
}

func TestCmdPriority_SavedAsLatestOverride(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> priority: high"})
	// Priority override stored via event log; verify it was logged.
	events, _ := store.ListEventsForItem(1)
	found := false
	for _, ev := range events {
		if ev.Kind == "priority_override" && strings.Contains(ev.Payload, "high") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected priority_override event")
	}
}

func TestCmdProject_TriggersRegenOnProposedItem(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 4)}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3, Worker: w}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	store.InsertProposal(&Proposal{ItemID: it.ID, SlackTS: "1700.p", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "new", Status: "draft"})

	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> project: nexus"})

	select {
	case j := <-w.queue:
		if _, ok := j.(ProposeJob); !ok {
			t.Fatalf("expected ProposeJob, got %T", j)
		}
	default:
		t.Fatal("expected ProposeJob enqueued after project override on proposed item")
	}
	// Old proposal should be rejected.
	p, _ := store.GetLatestProposal(it.ID)
	if p.Status != "rejected" {
		t.Fatalf("old proposal not rejected, status=%s", p.Status)
	}
}

func TestCmdPriority_TriggersRegenOnProposedItem(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 4)}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3, Worker: w}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	store.InsertProposal(&Proposal{ItemID: it.ID, SlackTS: "1700.p", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "new", Status: "draft"})

	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> priority: high"})

	select {
	case j := <-w.queue:
		if _, ok := j.(ProposeJob); !ok {
			t.Fatalf("expected ProposeJob, got %T", j)
		}
	default:
		t.Fatal("expected ProposeJob enqueued after priority override on proposed item")
	}
	// Old proposal should be rejected.
	p, _ := store.GetLatestProposal(it.ID)
	if p.Status != "rejected" {
		t.Fatalf("old proposal not rejected, status=%s", p.Status)
	}
}

func TestCmdRegen_EnqueuesProposeJob(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 1)}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3, Worker: w}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	store.InsertProposal(&Proposal{ItemID: it.ID, SlackTS: "1700.x", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "new", Status: "draft"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> regen"})
	select {
	case <-w.queue:
	default:
		t.Fatal("expected ProposeJob enqueued")
	}
	// Old proposal marked rejected.
	p, _ := store.GetLatestProposal(it.ID)
	if p.Status != "rejected" {
		t.Fatalf("old proposal status: %s", p.Status)
	}
}

func TestCmdFreeform_AsksClaude(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: "this item is about flaky tests."}
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels: map[string]bool{"C1": true},
		Signals:         &SignalsConfig{},
		ApprovalThreshold: 3,
		Claude:          fc,
	}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "flaky test in qompass"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> what's this about?"})
	if len(fake.posted) == 0 {
		t.Fatal("expected reply")
	}
}

func TestRouter_ProposalReactionFilesViaWorker(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	w := &Worker{queue: make(chan job, 4)}
	r := &Router{
		Store:             store,
		Slack:             fake,
		BotUserID:         "UBOT",
		WatchedChannels:   map[string]bool{"C1": true},
		Signals:           sig,
		ApprovalThreshold: 3,
		Worker:            w,
	}
	// Create an item.
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "do some work"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")

	// Seed a proposal with status=draft, recorded at a known ts.
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            "1700.prop",
		DraftJSON:          `{"summary":"s","description":"d","issue_type":"Task","labels":["deferred-work"],"priority":"Medium"}`,
		RelatedTicketsJSON: "[]",
		Branch:             "new",
		Status:             "draft",
	}
	store.InsertProposal(p)

	// Reaction added on the proposal message ts — should route through handleProposalReaction.
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.prop", Name: "white_check_mark"})

	select {
	case j := <-w.queue:
		fj, ok := j.(FileJob)
		if !ok {
			t.Fatalf("expected FileJob, got %T", j)
		}
		if fj.ProposalID != p.ID {
			t.Fatalf("expected proposalID %d, got %d", p.ID, fj.ProposalID)
		}
	default:
		t.Fatal("expected FileJob to be enqueued")
	}
}

func TestCmdStatus_ShowsVotersAndNextReminder(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels:   map[string]bool{"C1": true},
		Signals:           &SignalsConfig{ApproveReactions: []string{"white_check_mark"}},
		ApprovalThreshold: 3,
		ReminderInterval:  3 * 24 * time.Hour,
	}

	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	r.HandleReactionAdded(ReactionEvent{User: "U3", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})

	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U4", Text: "<@UBOT> status"})

	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
	body := fake.posted[len(fake.posted)-1].Text
	if !strings.Contains(body, "<@U2>") || !strings.Contains(body, "<@U3>") {
		t.Fatalf("expected voter mentions in status reply: %s", body)
	}
	if !strings.Contains(body, "Next reminder:") {
		t.Fatalf("expected next reminder ETA in status reply: %s", body)
	}
}

func TestCmdStatus_NoVoters(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels:   map[string]bool{"C1": true},
		Signals:           &SignalsConfig{},
		ApprovalThreshold: 3,
		ReminderInterval:  3 * 24 * time.Hour,
	}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> status"})
	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
	body := fake.posted[len(fake.posted)-1].Text
	if !strings.Contains(body, "none yet") {
		t.Fatalf("expected 'none yet' when no voters: %s", body)
	}
}

func TestCmdStatus_NoNextReminderForNonCollecting(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels:   map[string]bool{"C1": true},
		Signals:           &SignalsConfig{},
		ApprovalThreshold: 3,
		ReminderInterval:  3 * 24 * time.Hour,
	}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")

	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> status"})
	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
	body := fake.posted[len(fake.posted)-1].Text
	if strings.Contains(body, "Next reminder:") {
		t.Fatalf("next reminder should not appear for non-collecting item: %s", body)
	}
}

func TestRouter_ResolutionCommentKeyword(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	p := &Proposal{ItemID: it.ID, SlackTS: "1700.2", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "awaiting_resolution", ExistingTicketKey: "QORK-5", Status: "awaiting_resolution"}
	store.InsertProposal(p)
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.3", ThreadTS: "1700.1", User: "U2", Text: "comment please"})
	got, _ := store.GetLatestProposal(it.ID)
	if got.Branch != "comment_on_existing" {
		t.Fatalf("branch: %s", got.Branch)
	}
	if got.Status != "draft" {
		t.Fatalf("status: %s", got.Status)
	}
}
