package database

import (
	"time"

	"reisub-backend/models"

	"gorm.io/gorm"
)

// DeleteInactiveGuests removes guest users whose token has expired or whose
// last activity (or creation) is before cutoff, and returns how many.
//
// What happens to references:
//   - room_roles, room_microphone_locks and room_members rows of the guest
//     are deleted with it. A room left without an active owner hands
//     ownership to the next user who joins (see claimOwnershipOnJoin).
//   - chat_messages are kept: they carry the sender's display name, and
//     sender_id simply no longer matches a user.
//   - rooms.owner_id only records the creator and may point to a deleted
//     user.
//
// users.id is AUTOINCREMENT, so a deleted guest's ID is never reused and
// these leftovers can never attach to a new user. Times are compared in Go
// because SQLite stores them as text with the writer's UTC offset.
func DeleteInactiveGuests(db *gorm.DB, cutoff time.Time) (int64, error) {
	var guests []models.User
	if err := db.Select("id", "created_at", "last_seen_at", "token_expires_at").Where("is_guest = ?", true).Find(&guests).Error; err != nil {
		return 0, err
	}
	now := time.Now()
	var ids []uint
	for _, guest := range guests {
		lastActive := guest.CreatedAt
		if guest.LastSeenAt != nil {
			lastActive = *guest.LastSeenAt
		}
		expired := guest.TokenExpiresAt != nil && !now.Before(*guest.TokenExpiresAt)
		if expired || lastActive.Before(cutoff) {
			ids = append(ids, guest.ID)
		}
	}
	var deleted int64
	// Batches keep each statement's IN list well under SQLite's limits.
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, table := range []any{&models.RoomRole{}, &models.RoomMicrophoneLock{}, &models.RoomMember{}, &models.RoomBan{}, &models.WaitingRequest{}, &models.MeetingCooldown{}} {
				if err := tx.Where("user_id IN ?", batch).Delete(table).Error; err != nil {
					return err
				}
			}
			return tx.Where("is_guest = ?", true).Delete(&models.User{}, batch).Error
		})
		if err != nil {
			return deleted, err
		}
		deleted += int64(len(batch))
	}
	return deleted, nil
}
