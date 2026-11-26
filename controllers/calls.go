package controllers

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// callRingTime is how long a call rings before it counts as missed. Tests
// make it short.
var callRingTime = 45 * time.Second

// callsMu keeps a call's state changes in order: an answer, a decline and
// the end can come at the same moment.
var callsMu sync.Mutex

// callTimers end calls when their time is up.
var callTimers = map[uint]*time.Timer{}

// callEvent tells a device about a call: one that rings ("call"), stops
// ringing ("call_end", Missed when nobody answered), was turned down
// ("call_declined", By who) or went unanswered ("call_unanswered").
type callEvent struct {
	ID              uint   `json:"id"`
	RoomID          string `json:"room_id,omitempty"`
	Caller          string `json:"caller,omitempty"`
	CallerAvatarURL string `json:"caller_avatar_url,omitempty"`
	// Title names where the call is from: the caller, or the team.
	Title    string `json:"title,omitempty"`
	Kind     string `json:"kind,omitempty"`
	TargetID uint   `json:"target_id,omitempty"`
	EndsAt   string `json:"ends_at,omitempty"`
	// Secret lets this person's phone answer from its notification.
	Secret string `json:"secret,omitempty"`
	Missed bool   `json:"missed,omitempty"`
	By     string `json:"by,omitempty"`
}

// callPlace is where a call comes from, as the people called see it.
type callPlace struct {
	kind     string
	targetID uint
	title    string
}

// startCall rings everyone in invitees: their devices hear of it at once,
// and their phones get a push, until callRingTime passes.
func startCall(caller models.User, roomID, conversation string, place callPlace, invitees []uint) error {
	now := time.Now()
	call := models.Call{RoomID: roomID, CallerID: caller.ID, Conversation: conversation, EndsAt: now.Add(callRingTime)}
	secrets := map[uint]string{}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&call).Error; err != nil {
			return err
		}
		for _, id := range invitees {
			if id == caller.ID {
				continue
			}
			secret := middleware.GenerateToken()
			secrets[id] = secret
			if err := tx.Create(&models.CallInvitee{CallID: call.ID, UserID: id, State: models.CallRinging, SecretHash: models.HashToken(secret)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	scheduleCallEnd(call.ID, call.EndsAt)
	for id, secret := range secrets {
		event := callEvent{
			ID:              call.ID,
			RoomID:          roomID,
			Caller:          caller.Name(),
			CallerAvatarURL: avatarURL(caller.AvatarFile),
			Title:           place.title,
			Kind:            place.kind,
			TargetID:        place.targetID,
			EndsAt:          call.EndsAt.UTC().Format(time.RFC3339),
			Secret:          secret,
		}
		hub.send(id, realtimeEvent{Type: "call", Call: &event})
		pushToUser(id, map[string]string{
			"type":      "call",
			"call_id":   strconv.FormatUint(uint64(call.ID), 10),
			"room_id":   roomID,
			"caller":    event.Caller,
			"title":     event.Title,
			"kind":      event.Kind,
			"target_id": strconv.FormatUint(uint64(event.TargetID), 10),
			"ends_at":   event.EndsAt,
			"secret":    secret,
		}, callRingTime)
	}
	return nil
}

func scheduleCallEnd(id uint, at time.Time) {
	callsMu.Lock()
	defer callsMu.Unlock()
	if old := callTimers[id]; old != nil {
		old.Stop()
	}
	callTimers[id] = time.AfterFunc(time.Until(at), func() { finishCall(id) })
}

// ScheduleCallEnds ends, at their time, calls that were ringing when the
// server stopped; those already past end now.
func ScheduleCallEnds() error {
	var calls []models.Call
	if err := database.DB.Where("ended_at IS NULL").Find(&calls).Error; err != nil {
		return err
	}
	for _, call := range calls {
		scheduleCallEnd(call.ID, call.EndsAt)
	}
	return nil
}

// finishCall ends a call: whoever it still rings missed it, and their
// devices stop ringing. For a friend's call the caller hears nobody
// answered.
func finishCall(id uint) {
	callsMu.Lock()
	defer callsMu.Unlock()
	if timer := callTimers[id]; timer != nil {
		timer.Stop()
		delete(callTimers, id)
	}
	var call models.Call
	if err := database.DB.First(&call, id).Error; err != nil || call.EndedAt != nil {
		return
	}
	var ringing []models.CallInvitee
	if err := database.DB.Where("call_id = ? AND state = ?", id, models.CallRinging).Find(&ringing).Error; err != nil {
		log.Printf("warning: could not end call %d: %v", id, err)
		return
	}
	now := time.Now()
	if err := database.DB.Model(&models.CallInvitee{}).Where("call_id = ? AND state = ?", id, models.CallRinging).Update("state", models.CallMissed).Error; err != nil {
		log.Printf("warning: could not end call %d: %v", id, err)
		return
	}
	if err := database.DB.Model(&call).Update("ended_at", now).Error; err != nil {
		log.Printf("warning: could not end call %d: %v", id, err)
	}
	caller, _ := usersByID([]uint{call.CallerID})
	for _, invitee := range ringing {
		stopRinging(invitee.UserID, call, true, caller[call.CallerID])
	}
	if len(ringing) > 0 && strings.HasPrefix(call.Conversation, "friend:") {
		hub.send(call.CallerID, realtimeEvent{Type: "call_unanswered", Call: &callEvent{ID: call.ID, RoomID: call.RoomID}})
	}
}

// stopRinging tells the user's devices the call no longer rings there;
// missed leaves a missed call behind.
func stopRinging(userID uint, call models.Call, missed bool, caller models.User) {
	place := placeOf(call, userID, caller)
	hub.send(userID, realtimeEvent{Type: "call_end", Call: &callEvent{ID: call.ID, Missed: missed, Caller: caller.Name(), Title: place.title, Kind: place.kind, TargetID: place.targetID}})
	m := ""
	if missed {
		m = "1"
	}
	pushToUser(userID, map[string]string{
		"type":      "call_end",
		"call_id":   strconv.FormatUint(uint64(call.ID), 10),
		"missed":    m,
		"caller":    caller.Name(),
		"title":     place.title,
		"kind":      place.kind,
		"target_id": strconv.FormatUint(uint64(place.targetID), 10),
	}, time.Hour)
}

// placeOf is where a call is from, for one of the people it rang.
func placeOf(call models.Call, _ uint, caller models.User) callPlace {
	if id, ok := strings.CutPrefix(call.Conversation, "team:"); ok {
		teamID, _ := strconv.ParseUint(id, 10, 64)
		var team models.Team
		database.DB.First(&team, teamID)
		return callPlace{kind: "team", targetID: uint(teamID), title: team.Name}
	}
	return callPlace{kind: "friend", targetID: caller.ID, title: caller.Name()}
}

// endCallsBy ends the ringing calls a user made to a room, as when they
// leave the meeting before anyone answered; callerID 0 ends all of them.
func endCallsBy(roomID string, callerID uint) {
	var ids []uint
	q := database.DB.Model(&models.Call{}).Where("room_id = ? AND ended_at IS NULL", roomID)
	if callerID != 0 {
		q = q.Where("caller_id = ?", callerID)
	}
	if err := q.Pluck("id", &ids).Error; err != nil {
		log.Printf("warning: could not read the calls of room %s: %v", roomID, err)
		return
	}
	for _, id := range ids {
		finishCall(id)
	}
}

// AnswerCall is POST /calls/:call_id/answer: the user takes the call. It
// answers the room to join; the user's other devices stop ringing.
func AnswerCall(c *gin.Context) { respondToCall(c, models.CallAnswered) }

// DeclineCall is POST /calls/:call_id/decline. For a friend's call, the
// caller hears it was turned down.
func DeclineCall(c *gin.Context) { respondToCall(c, models.CallDeclined) }

// respondToCall takes the answer with the user's session, or, from a
// phone's notification without the app, with the call's secret for them.
func respondToCall(c *gin.Context, state string) {
	id, err := strconv.ParseUint(c.Param("call_id"), 10, 64)
	if err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.CallNotFound, "Arama bulunamadı")
		return
	}
	var input struct {
		Secret string `json:"secret"`
	}
	_ = c.ShouldBindJSON(&input)
	q := database.DB.Where("call_id = ?", id)
	if secret := strings.TrimSpace(input.Secret); secret != "" {
		q = q.Where("secret_hash = ?", models.HashToken(secret))
	} else {
		user, _, ok := middleware.UserForToken(middleware.BearerToken(c))
		if !ok {
			apierror.Respond(c, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
			return
		}
		q = q.Where("user_id = ?", user.ID)
	}
	callsMu.Lock()
	var invitees []models.CallInvitee
	if err := q.Limit(1).Find(&invitees).Error; err != nil {
		callsMu.Unlock()
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arama okunamadı")
		return
	}
	if len(invitees) == 0 {
		callsMu.Unlock()
		apierror.Respond(c, http.StatusNotFound, apierror.CallNotFound, "Arama bulunamadı")
		return
	}
	invitee := invitees[0]
	var call models.Call
	if err := database.DB.First(&call, invitee.CallID).Error; err != nil {
		callsMu.Unlock()
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arama okunamadı")
		return
	}
	wasRinging := invitee.State == models.CallRinging
	if wasRinging {
		if err := database.DB.Model(&invitee).Update("state", state).Error; err != nil {
			callsMu.Unlock()
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arama güncellenemedi")
			return
		}
	}
	var left int64
	database.DB.Model(&models.CallInvitee{}).Where("call_id = ? AND state = ?", call.ID, models.CallRinging).Count(&left)
	callsMu.Unlock()

	if wasRinging {
		callers, _ := usersByID([]uint{call.CallerID})
		stopRinging(invitee.UserID, call, false, callers[call.CallerID])
		if state == models.CallDeclined && strings.HasPrefix(call.Conversation, "friend:") {
			people, _ := usersByID([]uint{invitee.UserID})
			hub.send(call.CallerID, realtimeEvent{Type: "call_declined", Call: &callEvent{ID: call.ID, RoomID: call.RoomID, By: people[invitee.UserID].Name()}})
		}
		if left == 0 {
			finishCall(call.ID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"room_id": call.RoomID, "state": state})
}
