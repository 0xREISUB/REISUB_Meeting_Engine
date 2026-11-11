package controllers

import (
	"net/http"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// The waiting room. When it is on, POST /rooms/join answers 202 with a
// request_id for everyone except the owner, holders of admit_members and
// users admitted to the room before (they have a RoomMember row). The
// requester polls GET /waiting/:request_id until someone with admit_members
// admits or rejects them. A request whose requester has not polled for
// waitingExpiry is dropped, and all requests end with the room. Everything
// runs under the room lifecycle lock.

// waitingExpiry is how long a request survives without a poll. Clients poll
// every 2 s. Tests may change it.
var waitingExpiry = 30 * time.Second

type waitingEntry struct {
	RequestID string `json:"request_id"`
	Name      string `json:"name"`
}

// pruneWaiting drops the room's requests whose requester stopped polling.
func pruneWaiting(db *gorm.DB, roomID string) error {
	return db.Where("room_id = ? AND last_polled_at < ?", roomID, time.Now().Add(-waitingExpiry)).Delete(&models.WaitingRequest{}).Error
}

// isAdmitted reports whether the user was let into the room before: they
// created or joined it, or were admitted from the waiting room.
func isAdmitted(roomID string, userID uint) (bool, error) {
	var count int64
	err := database.DB.Model(&models.RoomMember{}).Where("room_id = ? AND user_id = ?", roomID, userID).Count(&count).Error
	return count > 0, err
}

// skipsWaitingRoom reports whether the user joins directly although the
// waiting room is on: the owner (even while away), holders of
// admit_members, and users admitted before.
func skipsWaitingRoom(roomID string, userID uint) (bool, error) {
	row, err := roleRow(database.DB, roomID, userID)
	if err != nil {
		return false, err
	}
	if row != nil && row.Role == models.RoleOwner || rowAccess(row).has(models.PermissionAdmitMembers) {
		return true, nil
	}
	return isAdmitted(roomID, userID)
}

// requestToWait files (or refreshes) the user's waiting request and returns
// its ID. A user who is still waiting keeps their request ID; after a
// rejection they get a new request.
func requestToWait(roomID string, userID uint, name string) (string, error) {
	var requestID string
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := pruneWaiting(tx, roomID); err != nil {
			return err
		}
		var rows []models.WaitingRequest
		if err := tx.Where("room_id = ? AND user_id = ?", roomID, userID).Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		now := time.Now()
		if len(rows) > 0 && rows[0].Status == models.WaitingStatusWaiting {
			requestID = rows[0].RequestID
			return tx.Model(&rows[0]).Updates(map[string]interface{}{"name": name, "last_polled_at": now}).Error
		}
		if len(rows) > 0 {
			if err := tx.Delete(&rows[0]).Error; err != nil {
				return err
			}
		}
		requestID = uuid.NewString()
		return tx.Create(&models.WaitingRequest{RequestID: requestID, RoomID: roomID, UserID: userID, Name: name, Status: models.WaitingStatusWaiting, LastPolledAt: now}).Error
	})
	return requestID, err
}

// waitingList is the room's open requests, oldest first.
func waitingList(roomID string) ([]waitingEntry, error) {
	if err := pruneWaiting(database.DB, roomID); err != nil {
		return nil, err
	}
	var rows []models.WaitingRequest
	if err := database.DB.Where("room_id = ? AND status = ?", roomID, models.WaitingStatusWaiting).Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	list := []waitingEntry{}
	for _, row := range rows {
		list = append(list, waitingEntry{RequestID: row.RequestID, Name: row.Name})
	}
	return list, nil
}

// meetingState is the /state body as the caller may see it: the waiting
// list only for the owner and holders of admit_members.
func meetingState(c *gin.Context, room models.Room) (gin.H, error) {
	waiting := []waitingEntry{}
	caller, err := roomAccess(room.RoomID, c.MustGet("user").(models.User).ID)
	if err != nil {
		return nil, err
	}
	if caller.has(models.PermissionAdmitMembers) {
		if waiting, err = waitingList(room.RoomID); err != nil {
			return nil, err
		}
	}
	state := gin.H{"screen_share_identity": room.ScreenShareIdentity, "waiting_room": room.WaitingRoom, "waiting": waiting}
	if secondsLeft, endsAt := timeLeft(room); secondsLeft != nil {
		state["seconds_left"], state["ends_at"] = *secondsLeft, *endsAt
	}
	return state, nil
}

// respondState writes the caller's /state body with status 200.
func respondState(c *gin.Context, room models.Room) {
	state, err := meetingState(c, room)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	c.JSON(http.StatusOK, state)
}

// admitRequest lets a waiting user in: they count as admitted to the room
// from now on, under the name they asked with.
func admitRequest(tx *gorm.DB, request *models.WaitingRequest) error {
	if err := tx.Model(request).Updates(map[string]interface{}{"status": models.WaitingStatusAdmitted, "last_polled_at": time.Now()}).Error; err != nil {
		return err
	}
	return rememberDisplayNameTx(tx, request.RoomID, request.UserID, request.Name)
}

// UpdateMeetingSettings turns the waiting room on or off. Turning it off
// admits everyone who is waiting.
func UpdateMeetingSettings(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, _, ok := requirePermission(c, models.PermissionAdmitMembers, false)
	if !ok {
		return
	}
	var input struct {
		WaitingRoom *bool `json:"waiting_room"`
	}
	if c.ShouldBindJSON(&input) != nil || input.WaitingRoom == nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Bekleme odası ayarı gerekli")
		return
	}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&room).Update("waiting_room", *input.WaitingRoom).Error; err != nil {
			return err
		}
		if *input.WaitingRoom {
			return nil
		}
		if err := pruneWaiting(tx, room.RoomID); err != nil {
			return err
		}
		var waiting []models.WaitingRequest
		if err := tx.Where("room_id = ? AND status = ?", room.RoomID, models.WaitingStatusWaiting).Find(&waiting).Error; err != nil {
			return err
		}
		for i := range waiting {
			if err := admitRequest(tx, &waiting[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Ayar kaydedilemedi")
		return
	}
	room.WaitingRoom = *input.WaitingRoom
	respondState(c, room)
}

// waitingRequest loads the request named in the path. Requests that expired
// count as gone.
func waitingRequest(c *gin.Context, roomID string) (*models.WaitingRequest, bool) {
	if err := pruneWaiting(database.DB, roomID); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return nil, false
	}
	var rows []models.WaitingRequest
	if err := database.DB.Where("room_id = ? AND request_id = ?", roomID, c.Param("request_id")).Limit(1).Find(&rows).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return nil, false
	}
	if len(rows) == 0 {
		respondWaitingNotFound(c)
		return nil, false
	}
	return &rows[0], true
}

func respondWaitingNotFound(c *gin.Context) {
	apierror.Respond(c, http.StatusNotFound, apierror.WaitingRequestNotFound, "Katılma isteği bulunamadı")
}

// ownWaitingRequest is waitingRequest for its requester only; to anyone else
// the request does not exist.
func ownWaitingRequest(c *gin.Context) (models.Room, *models.WaitingRequest, bool) {
	room, ok := activeRoom(c)
	if !ok {
		return room, nil, false
	}
	request, ok := waitingRequest(c, room.RoomID)
	if !ok {
		return room, nil, false
	}
	if request.UserID != c.MustGet("user").(models.User).ID {
		respondWaitingNotFound(c)
		return room, nil, false
	}
	return room, request, true
}

// GetWaitingRequest is polled by the requester. Once admitted, it answers
// with fresh join credentials.
func GetWaitingRequest(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, request, ok := ownWaitingRequest(c)
	if !ok {
		return
	}
	if err := database.DB.Model(request).Update("last_polled_at", time.Now()).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	if request.Status != models.WaitingStatusAdmitted {
		c.JSON(http.StatusOK, gin.H{"status": request.Status})
		return
	}
	if !rejectBanned(c, room.RoomID) {
		return
	}
	response, ok := enterRoom(c, &room, request.UserID, request.Name)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, admittedResponse{Status: models.WaitingStatusAdmitted, roomResponse: response})
}

// admittedResponse is a join response marked as admitted.
type admittedResponse struct {
	Status string `json:"status"`
	roomResponse
}

// CancelWaitingRequest lets the requester give up.
func CancelWaitingRequest(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	_, request, ok := ownWaitingRequest(c)
	if !ok {
		return
	}
	if err := database.DB.Delete(request).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İstek silinemedi")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

// AdmitWaitingRequest lets a waiting user in.
func AdmitWaitingRequest(c *gin.Context) { answerWaiting(c, true) }

// RejectWaitingRequest turns a waiting user away. It is not a ban: they may
// ask again.
func RejectWaitingRequest(c *gin.Context) { answerWaiting(c, false) }

func answerWaiting(c *gin.Context, admit bool) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, _, ok := requirePermission(c, models.PermissionAdmitMembers, false)
	if !ok {
		return
	}
	request, ok := waitingRequest(c, room.RoomID)
	if !ok {
		return
	}
	if request.Status == models.WaitingStatusWaiting {
		var err error
		if admit {
			err = database.DB.Transaction(func(tx *gorm.DB) error { return admitRequest(tx, request) })
		} else {
			err = database.DB.Model(request).Update("status", models.WaitingStatusRejected).Error
		}
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İstek yanıtlanamadı")
			return
		}
	}
	respondState(c, room)
}
