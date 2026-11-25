package controllers

import (
	"log"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/livekit/protocol/livekit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// markInMeeting records that the user behind identity joined the room's
// media session, so friends see them in a meeting and the room's team sees
// it going on.
func markInMeeting(roomID, identity string) {
	userID, err := participantUserID(identity)
	if err != nil {
		return
	}
	row := models.MeetingPresence{RoomID: roomID, UserID: userID}
	if err := database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		log.Printf("warning: could not record user %d in room %s: %v", userID, roomID, err)
	}
}

// recordMeetingPresence rewrites who is in the room from LiveKit's list of
// connected sessions, leaving out those gone reports as just departed.
func recordMeetingPresence(roomID string, ps []*livekit.ParticipantInfo, gone func(*livekit.ParticipantInfo) bool) {
	present := map[uint]bool{}
	for _, p := range ps {
		if gone != nil && gone(p) {
			continue
		}
		if id, err := participantUserID(p.Identity); err == nil {
			present[id] = true
		}
	}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("room_id = ?", roomID).Delete(&models.MeetingPresence{}).Error; err != nil {
			return err
		}
		for id := range present {
			if err := tx.Create(&models.MeetingPresence{RoomID: roomID, UserID: id}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("warning: could not record who is in room %s: %v", roomID, err)
	}
}

// roomsInUse reports which of the given rooms are active with someone
// connected.
func roomsInUse(roomIDs []string) (map[string]bool, error) {
	result := map[string]bool{}
	if len(roomIDs) == 0 {
		return result, nil
	}
	var ids []string
	err := database.DB.Model(&models.MeetingPresence{}).
		Joins("JOIN rooms ON rooms.room_id = meeting_presences.room_id AND rooms.status = ?", models.RoomStatusActive).
		Where("meeting_presences.room_id IN ?", roomIDs).
		Distinct().Pluck("meeting_presences.room_id", &ids).Error
	for _, id := range ids {
		result[id] = true
	}
	return result, err
}
