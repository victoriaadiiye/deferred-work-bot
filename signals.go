package main

import (
	"regexp"
	"strings"
	"sync"
)

func IsApproveReaction(sig *SignalsConfig, emoji string) bool {
	return contains(sig.ApproveReactions, emoji)
}

func IsCancelReaction(sig *SignalsConfig, emoji string) bool {
	return contains(sig.CancelReactions, emoji)
}

func ReplyHasApprove(sig *SignalsConfig, text string) bool {
	return anyWordMatch(text, sig.ApproveReplies)
}

func ReplyHasCancel(sig *SignalsConfig, text string) bool {
	return anyWordMatch(text, sig.CancelReplies)
}

// ResolutionKeyword scans a reply for the first of: comment, new, both.
// Returns "" if none found.
func ResolutionKeyword(text string) string {
	t := strings.ToLower(text)
	for _, kw := range []string{"both", "comment", "new"} {
		if wordMatch(t, kw) {
			return kw
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func anyWordMatch(text string, tokens []string) bool {
	t := strings.ToLower(text)
	for _, tok := range tokens {
		if wordMatch(t, strings.ToLower(tok)) {
			return true
		}
	}
	return false
}

var (
	wordCacheMu sync.RWMutex
	wordCache   = map[string]*regexp.Regexp{}
)

func wordMatch(text, token string) bool {
	wordCacheMu.RLock()
	re, ok := wordCache[token]
	wordCacheMu.RUnlock()
	if !ok {
		// allow '+', alphanumerics, dashes; word-boundary that treats '+' and '-' as word chars
		compiled := regexp.MustCompile(`(^|[^\w\-+])` + regexp.QuoteMeta(token) + `($|[^\w\-+])`)
		wordCacheMu.Lock()
		// Check again under write lock in case another goroutine compiled it.
		if re, ok = wordCache[token]; !ok {
			re = compiled
			wordCache[token] = re
		}
		wordCacheMu.Unlock()
	}
	return re.MatchString(text)
}
