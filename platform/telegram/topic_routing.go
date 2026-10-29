package telegram

import (
	"strconv"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/go-telegram/bot/models"
)

const (
	privateTopicFollowSeconds = 5
	privateTopicRouteLimit    = 256
	privateTopicRouteTTL      = 10 * time.Minute
)

type privateTopicRouteKey struct {
	chatID  int64
	userID  int64
	albumID string
}

type privateTopicRoute struct {
	threadID  int
	messageID int
	sentAt    int
	seenAt    time.Time
	opened    bool
	ambiguous bool
}

// privateTopicRouting follows a newly created private topic until Telegram
// supplies an explicit thread ID or the creator's send timestamps diverge.
// A zero threadID retains ordering information without adopting more messages.
type privateTopicRouting struct {
	mu     sync.Mutex
	routes map[privateTopicRouteKey]privateTopicRoute
}

// routeTopicMessage consumes topic-creation service messages and fills in a
// missing thread ID on their private-chat openers. Call it before discarding old
// updates too: a creation may precede the restart cutoff while its opener does
// not. It performs no network requests and never creates topics or agent turns.
// An ambiguous result must not be dispatched to a general-chat agent session.
func (p *Platform) routeTopicMessage(msg *models.Message) (service, ambiguous bool) {
	service = msg.ForumTopicCreated != nil
	if msg.Chat.Type != models.ChatTypePrivate || msg.From == nil || msg.From.IsBot ||
		msg.ID <= 0 || msg.Date <= 0 || !core.AllowList(p.allowFrom, strconv.FormatInt(msg.From.ID, 10)) {
		return service, false
	}
	if !service && !isTopicContent(msg) {
		return false, false
	}

	return service, p.topicRouting.route(msg, time.Now())
}

func isTopicContent(msg *models.Message) bool {
	return msg.Text != "" || len(msg.Photo) > 0 || msg.Document != nil ||
		msg.Voice != nil || msg.Audio != nil || msg.Location != nil
}

func (r *privateTopicRouting) route(msg *models.Message, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes == nil {
		r.routes = make(map[privateTopicRouteKey]privateTopicRoute)
	}
	r.expire(now)
	key := privateTopicRouteKey{chatID: msg.Chat.ID, userID: msg.From.ID}
	previous := r.routes[key]
	// IDs need only increase, never be adjacent: private-chat message IDs are
	// shared with the bot's other private chats and outgoing messages.
	if msg.ID <= previous.messageID {
		return previous.ambiguous && msg.MessageThreadID == 0
	}

	next := privateTopicRoute{messageID: msg.ID, sentAt: msg.Date, seenAt: now}
	if msg.ForumTopicCreated != nil {
		if msg.Date < previous.sentAt {
			return false
		}
		next.threadID = msg.MessageThreadID
		if msg.Date-previous.sentAt <= privateTopicFollowSeconds &&
			(previous.ambiguous || (previous.threadID > 0 && !previous.opened)) {
			// Two creations before either opener carry no evidence identifying
			// which topic a thread-less message belongs to. Never guess latest.
			next.threadID = 0
			next.ambiguous = true
		}
		r.remember(key, next)
		return false
	}

	albumKey := key
	albumKey.albumID = msg.MediaGroupID
	album := r.routes[albumKey]
	knownAlbum := msg.MediaGroupID != "" && album.follows(msg) &&
		(msg.MessageThreadID == 0 || msg.MessageThreadID == album.threadID)
	if knownAlbum && previous.threadID != album.threadID {
		// An older album can continue after a newer topic was created. Route
		// that known album without consuming or extending the new opener hint.
		msg.MessageThreadID = album.threadID
		album.messageID, album.sentAt, album.seenAt = msg.ID, msg.Date, now
		r.remember(albumKey, album)
		previous.messageID, previous.seenAt = msg.ID, now
		r.remember(key, previous)
		return false
	}
	if msg.Date < previous.sentAt {
		return false
	}
	if msg.MessageThreadID == 0 {
		if previous.ambiguous && msg.Date-previous.sentAt <= privateTopicFollowSeconds {
			// Rejections do not extend the creation's correlation window.
			// Outside it, remember next below to clear expired ambiguity.
			previous.messageID, previous.seenAt = msg.ID, now
			r.remember(key, previous)
			return true
		}
		if previous.follows(msg) {
			msg.MessageThreadID = previous.threadID
			// Keep following split text and media sent in the same short burst.
			next.threadID = previous.threadID
			next.opened = true
		}
	}
	// Explicit IDs end the opener fallback. An album retains its own hint so
	// later members can omit the ID even when its first member included it.
	if msg.MediaGroupID != "" && msg.MessageThreadID != 0 {
		album := next
		album.threadID = msg.MessageThreadID
		r.remember(albumKey, album)
	}
	r.remember(key, next)
	return false
}

func (r privateTopicRoute) follows(msg *models.Message) bool {
	return r.threadID > 0 && msg.ID > r.messageID && msg.Date >= r.sentAt &&
		msg.Date-r.sentAt <= privateTopicFollowSeconds
}

// Wall-clock expiry bounds memory only. Adoption uses Telegram send times, so
// a download or transcription delaying handler execution does not age out an
// opener or the next chunk of a split message.
func (r *privateTopicRouting) expire(now time.Time) {
	for key, route := range r.routes {
		if now.Sub(route.seenAt) > privateTopicRouteTTL {
			delete(r.routes, key)
		}
	}
}

func (r *privateTopicRouting) remember(key privateTopicRouteKey, route privateTopicRoute) {
	if _, exists := r.routes[key]; !exists && len(r.routes) >= privateTopicRouteLimit {
		var oldestKey privateTopicRouteKey
		var oldest time.Time
		for candidate, record := range r.routes {
			if oldest.IsZero() || record.seenAt.Before(oldest) {
				oldestKey, oldest = candidate, record.seenAt
			}
		}
		delete(r.routes, oldestKey)
	}
	r.routes[key] = route
}
