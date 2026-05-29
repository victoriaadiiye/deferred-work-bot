package main

import "github.com/slack-go/slack"

type SlackAPI interface {
	PostMessage(channelID string, options ...slack.MsgOption) (channel string, ts string, err error)
	AddReaction(name string, item slack.ItemRef) error
	RemoveReaction(name string, item slack.ItemRef) error
	GetConversationReplies(params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
	GetPermalink(params *slack.PermalinkParameters) (string, error)
	AuthTest() (*slack.AuthTestResponse, error)
}
