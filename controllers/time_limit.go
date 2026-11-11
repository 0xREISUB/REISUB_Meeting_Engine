package controllers

import (
	"context"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"
	"reisub-backend/policy"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The meeting time limit. A room created while MEETING_TIME_LIMIT_MINUTES is
// set gets EndsAt = creation + limit, and a timer ends it then exactly like
// POST /close. Its creator and its owner at that moment may not create a new
// room until EndsAt + MEETING_COOLDOWN_MINUTES. Rooms ended any other way
// start no wait.

// endRetry is how soon a failed end (LiveKit unreachable) is tried again.
// afterFunc is time.AfterFunc; tests may replace both.
var (
	endRetry  = 30 * time.Second
	afterFunc = time.AfterFunc
)

// timeLeft is the remaining time of a limited room as the API reports it:
// whole seconds, never negative, and the end in RFC 3339 UTC. Both are nil
// for rooms without a limit.
func timeLeft(room models.Room) (*int64, *string) {
	if room.EndsAt == nil {
		return nil, nil
	}
	seconds := int64(time.Until(*room.EndsAt) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	endsAt := room.EndsAt.UTC().Format(time.RFC3339)
	return &seconds, &endsAt
}

// pastLimit reports whether a limited room's time is up.
func pastLimit(room models.Room) bool {
	return room.EndsAt != nil && !time.Now().Before(*room.EndsAt)
}

// scheduleRoomEnd starts the timer that ends a limited room.
func scheduleRoomEnd(roomID string, endsAt time.Time) {
	afterFunc(time.Until(endsAt), func() { endRoomAtLimit(roomID) })
}

// ScheduleRoomEnds runs at startup: it ends every active room already past
// its limit and schedules the rest, so a restart never skips or extends a
// limit.
func ScheduleRoomEnds() error {
	var rooms []models.Room
	if err := database.DB.Where("status = ? AND ends_at IS NOT NULL", models.RoomStatusActive).Find(&rooms).Error; err != nil {
		return err
	}
	for _, room := range rooms {
		scheduleRoomEnd(room.RoomID, *room.EndsAt)
	}
	return nil
}

// endRoomAtLimit closes a room whose time is up and holds back its creator
// and current owner. A room closed meanwhile is left alone; a failure is
// retried after endRetry.
func endRoomAtLimit(roomID string) {
	ended, err := tryEndRoomAtLimit(roomID)
	if err != nil {
		log.Printf("warning: could not end room %s at its time limit, retrying: %v", roomID, err)
		afterFunc(endRetry, func() { endRoomAtLimit(roomID) })
		return
	}
	if ended {
		log.Printf("room %s ended at its time limit", roomID)
	}
}

func tryEndRoomAtLimit(roomID string) (bool, error) {
	release := lockRoomLifecycle(roomID)
	defer release()
	var room models.Room
	if err := database.DB.Where("room_id = ?", roomID).Limit(1).Find(&room).Error; err != nil {
		return false, err
	}
	if room.ID == 0 || room.Status == models.RoomStatusClosed || room.EndsAt == nil {
		return false, nil
	}
	if !pastLimit(room) {
		// The timer fired early (it should not): try again at the end.
		scheduleRoomEnd(roomID, *room.EndsAt)
		return false, nil
	}
	// Hold back the creator and whoever owns the room now (an acting owner
	// while the owner is away).
	held := map[uint]bool{room.OwnerID: true}
	var owners []models.RoomRole
	if err := database.DB.Where("room_id = ? AND role = ? AND suspended = ?", roomID, models.RoleOwner, false).Find(&owners).Error; err != nil {
		return false, err
	}
	for _, owner := range owners {
		held[owner.UserID] = true
	}
	ctx, svc, err := connectLiveKit(context.Background(), roomID)
	if err != nil {
		return false, err
	}
	if _, err = closeRoom(ctx, svc, &room); err != nil {
		return false, err
	}
	until := room.EndsAt.Add(policy.MeetingCooldown())
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&room).Update("ended_at_limit", true).Error; err != nil {
			return err
		}
		if !until.After(*room.EndsAt) {
			return nil
		}
		for userID := range held {
			if userID == 0 {
				continue
			}
			if err := holdBack(tx, userID, until); err != nil {
				return err
			}
		}
		return nil
	})
	return true, err
}

// holdBack keeps userID from creating rooms until at least until; a longer
// wait already in place is kept.
func holdBack(tx *gorm.DB, userID uint, until time.Time) error {
	var rows []models.MeetingCooldown
	if err := tx.Where("user_id = ?", userID).Limit(1).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return tx.Create(&models.MeetingCooldown{UserID: userID, Until: until}).Error
	}
	if rows[0].Until.After(until) {
		return nil
	}
	return tx.Model(&rows[0]).Update("until", until).Error
}

// cooldownLeft is how long the user must still wait before creating a room.
func cooldownLeft(userID uint) (time.Duration, error) {
	var rows []models.MeetingCooldown
	if err := database.DB.Where("user_id = ?", userID).Limit(1).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return time.Until(rows[0].Until), nil
}

// rejectCooldown answers 429 meeting_cooldown while the caller waits after a
// meeting that ended at its time limit, and reports whether to continue.
func rejectCooldown(c *gin.Context, userID uint) bool {
	left, err := cooldownLeft(userID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda oluşturulamadı")
		return false
	}
	if left <= 0 {
		return true
	}
	seconds := int64(math.Ceil(left.Seconds()))
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	apierror.RespondWith(c, http.StatusTooManyRequests, apierror.MeetingCooldown,
		"Süre sınırına ulaşan toplantınızdan sonra yeni toplantı oluşturmak için biraz beklemelisiniz",
		map[string]any{"retry_after_seconds": seconds})
	return false
}
