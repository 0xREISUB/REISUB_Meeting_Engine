package controllers

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gin-gonic/gin"
)

const (
	// realtimeAuthTimeout is how long a new connection has to say who it
	// is.
	realtimeAuthTimeout = 10 * time.Second
	// realtimePing keeps the connection, and every proxy on its way,
	// from counting it idle.
	realtimePing      = 25 * time.Second
	realtimeWriteTime = 10 * time.Second
)

// Close codes a connection ends with when it cannot stay.
const (
	closeAuthRequired    websocket.StatusCode = 4400
	closeInvalidToken    websocket.StatusCode = 4401
	closeAccountRequired websocket.StatusCode = 4403
	// closeRoomGone: no meeting the user may watch by that code: unknown,
	// ended, or one they are not in (or were removed from). All look the
	// same, so watching tells nothing about which codes exist.
	closeRoomGone websocket.StatusCode = 4404
	// closeRateLimited: too many refused codes from this address; the
	// same limit as wrong codes under /rooms.
	closeRateLimited websocket.StatusCode = 4429
)

// RoomMisses counts, per client IP, room codes that came to nothing: 404s
// under /rooms and meetings refused to watch here. The routes share theirs.
var RoomMisses = middleware.NewKeyLimiter(30, 10*time.Minute)

func init() {
	hub.onPresence = presenceChanged
}

// realtimeHandlers counts the open connections' handlers, which run on
// after the HTTP server lets go of them; tests wait for them to finish.
var realtimeHandlers sync.WaitGroup

// RealtimeSocket is the app's realtime connection. It first reads
// {"type":"auth","token":...}, answers {"type":"ready"}, then sends events
// ({"type":"message",...} or {"type":"social"}) as they happen. The token
// travels in a message rather than the URL, so no log ever holds it.
//
// With "room" in the auth message the connection watches that meeting
// instead, for anyone in it, guests too: it sends
// {"type":"room","room":...,"what":"chat"|"state"} when something there
// changes, and nothing social.
func RealtimeSocket(c *gin.Context) {
	realtimeHandlers.Add(1)
	defer realtimeHandlers.Done()
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4096)
	ctx := c.Request.Context()

	authCtx, cancel := context.WithTimeout(ctx, realtimeAuthTimeout)
	var hello struct {
		Type  string `json:"type"`
		Token string `json:"token"`
		Room  string `json:"room"`
	}
	err = wsjson.Read(authCtx, conn, &hello)
	cancel()
	if err != nil || hello.Type != "auth" {
		conn.Close(closeAuthRequired, "auth required")
		return
	}
	user, _, ok := middleware.UserForToken(strings.TrimSpace(hello.Token))
	if !ok {
		conn.Close(closeInvalidToken, "invalid token")
		return
	}
	client := &realtimeClient{userID: user.ID, send: make(chan []byte, realtimeQueue), gone: make(chan struct{})}
	if roomID := strings.TrimSpace(hello.Room); roomID != "" {
		ip := c.ClientIP()
		if over, _ := RoomMisses.Exhausted(ip); over {
			conn.Close(closeRateLimited, "rate limited")
			return
		}
		if !mayWatchRoom(roomID, user.ID) {
			RoomMisses.Add(ip)
			conn.Close(closeRoomGone, "room gone")
			return
		}
		watchers.add(roomID, client)
		defer watchers.remove(roomID, client)
	} else {
		if user.IsGuest {
			conn.Close(closeAccountRequired, "account required")
			return
		}
		hub.add(client)
		defer hub.remove(client)
	}
	if err := wsjson.Write(ctx, conn, map[string]string{"type": "ready"}); err != nil {
		return
	}
	// The app sends nothing more; this notices it going away.
	readCtx := conn.CloseRead(ctx)
	ping := time.NewTicker(realtimePing)
	defer ping.Stop()
	for {
		select {
		case <-readCtx.Done():
			return
		case <-client.gone:
			// Too slow to keep up: it reconnects and catches up.
			conn.Close(websocket.StatusTryAgainLater, "too slow")
			return
		case data := <-client.send:
			writeCtx, cancel := context.WithTimeout(readCtx, realtimeWriteTime)
			err := conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(readCtx, realtimeWriteTime)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// mayWatchRoom reports whether the user may watch the active meeting: they
// hold a role in it (the owner, or granted rights), joined or were let into
// it, or are waiting to be let in now; and were not removed from it. A
// code alone is not enough.
func mayWatchRoom(roomID string, userID uint) bool {
	if !activeRoomExists(roomID) {
		return false
	}
	if banned, err := isBanned(roomID, userID); err != nil || banned {
		return false
	}
	if row, err := roleRow(database.DB, roomID, userID); err != nil {
		return false
	} else if row != nil {
		return true
	}
	if admitted, err := isAdmitted(roomID, userID); err != nil {
		return false
	} else if admitted {
		return true
	}
	var waiting int64
	err := database.DB.Model(&models.WaitingRequest{}).
		Where("room_id = ? AND user_id = ? AND status = ?", roomID, userID, models.WaitingStatusWaiting).
		Count(&waiting).Error
	return err == nil && waiting > 0
}

// presenceChanged tells a user's friends and teams they came online or
// went away; going away also records when, for "last seen".
func presenceChanged(userID uint, online bool, at time.Time) {
	if !online {
		if err := database.DB.Model(&models.User{}).Where("id = ?", userID).UpdateColumn("active_at", at).Error; err != nil {
			log.Printf("warning: could not record when user %d left: %v", userID, err)
		}
	}
	socialChanged(contactsOf(userID)...)
}

// contactsOf answers who sees the user: their friends and everyone in
// their teams.
func contactsOf(userID uint) []uint {
	var ids []uint
	var rows []models.Friendship
	if err := database.DB.Where("(user_id = ? OR friend_id = ?) AND status = ?", userID, userID, models.FriendshipAccepted).Find(&rows).Error; err != nil {
		log.Printf("warning: could not read friends of user %d: %v", userID, err)
	}
	for _, row := range rows {
		ids = append(ids, otherSide(row, userID))
	}
	var teammates []uint
	if err := database.DB.Model(&models.TeamMember{}).
		Where("team_id IN (?) AND user_id <> ?", database.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID), userID).
		Distinct().Pluck("user_id", &teammates).Error; err != nil {
		log.Printf("warning: could not read teammates of user %d: %v", userID, err)
	}
	return append(ids, teammates...)
}

// teamMemberIDs answers everyone in the team.
func teamMemberIDs(teamID uint) []uint {
	var ids []uint
	if err := database.DB.Model(&models.TeamMember{}).Where("team_id = ?", teamID).Pluck("user_id", &ids).Error; err != nil {
		log.Printf("warning: could not read members of team %d: %v", teamID, err)
	}
	return ids
}

// roomTeamMembers answers the members of the teams whose meeting room this
// is, who see whether it is going on.
func roomTeamMembers(roomID string) []uint {
	var ids []uint
	if err := database.DB.Model(&models.TeamMember{}).
		Joins("JOIN teams ON teams.id = team_members.team_id AND teams.room_id = ?", roomID).
		Distinct().Pluck("team_members.user_id", &ids).Error; err != nil {
		log.Printf("warning: could not read the team of room %s: %v", roomID, err)
	}
	return ids
}

// publishMessage sends a new message at once to everyone in its
// conversation, each as they see it: theirs or not, and where it was sent.
func publishMessage(message models.SocialMessage) {
	type recipient struct {
		userID uint
		info   conversationInfo
	}
	var recipients []recipient
	switch {
	case strings.HasPrefix(message.Conversation, "friend:"):
		parts := strings.Split(message.Conversation, ":")
		if len(parts) != 3 {
			return
		}
		a, errA := strconv.ParseUint(parts[1], 10, 64)
		b, errB := strconv.ParseUint(parts[2], 10, 64)
		if errA != nil || errB != nil {
			return
		}
		users, err := usersByID([]uint{uint(a), uint(b)})
		if err != nil {
			return
		}
		for _, pair := range [][2]uint{{uint(a), uint(b)}, {uint(b), uint(a)}} {
			recipients = append(recipients, recipient{pair[0], conversationInfo{Kind: "friend", TargetID: pair[1], Title: users[pair[1]].Name()}})
		}
	case strings.HasPrefix(message.Conversation, "team:"):
		id, err := strconv.ParseUint(strings.TrimPrefix(message.Conversation, "team:"), 10, 64)
		if err != nil {
			return
		}
		var team models.Team
		if err := database.DB.First(&team, id).Error; err != nil {
			return
		}
		for _, member := range teamMemberIDs(team.ID) {
			recipients = append(recipients, recipient{member, conversationInfo{Kind: "team", TargetID: team.ID, Title: team.Name}})
		}
	}
	for _, r := range recipients {
		formatted, err := formatSocialMessages([]models.SocialMessage{message}, r.userID)
		if err != nil || len(formatted) == 0 {
			continue
		}
		notification := notificationMessage{
			socialMessageResponse: formatted[0],
			Kind:                  r.info.Kind,
			TargetID:              r.info.TargetID,
			Title:                 r.info.Title,
		}
		hub.send(r.userID, realtimeEvent{Type: "message", Message: &notification})
		if r.userID != message.SenderID {
			pushNewMessage(r.userID, notification)
		}
	}
}
