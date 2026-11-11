package controllers

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"gorm.io/gorm"
)

// LiveKit attributes carrying each participant's role ("owner" or
// "participant") and permissions (comma-separated, wire order) to every
// client.
const (
	roleAttribute        = "meeting.role"
	permissionsAttribute = "meeting.permissions"
)

// access is what a user may currently do in a room.
type access struct {
	Role        string
	Permissions []string
}

func (a access) has(permission string) bool {
	return slices.Contains(a.Permissions, permission)
}

func ownerAccess() access {
	return access{Role: models.RoleOwner, Permissions: models.AllPermissions}
}

func participantAccess(permissions string) access {
	return access{Role: models.RoleParticipant, Permissions: models.ParsePermissions(permissions)}
}

func (a access) attributes() map[string]string {
	return map[string]string{roleAttribute: a.Role, permissionsAttribute: models.JoinPermissions(a.Permissions)}
}

// rowAccess is the access a role row grants. A suspended owner has no powers
// until they rejoin.
func rowAccess(row *models.RoomRole) access {
	switch {
	case row == nil:
		return participantAccess("")
	case row.Role == models.RoleOwner && !row.Suspended:
		return ownerAccess()
	case row.Role == models.RoleOwner:
		return participantAccess("")
	}
	return participantAccess(row.Permissions)
}

// roleRow returns the user's role row in the room, or nil if they have none.
func roleRow(db *gorm.DB, roomID string, userID uint) (*models.RoomRole, error) {
	// Find instead of First: a missing row is normal and must not be logged.
	var rows []models.RoomRole
	if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// roomAccess is the access a user currently has in a room.
func roomAccess(roomID string, userID uint) (access, error) {
	row, err := roleRow(database.DB, roomID, userID)
	if err != nil {
		return access{}, err
	}
	return rowAccess(row), nil
}

// isProtectedTarget reports whether nobody may act on target: the caller
// themselves and the room owner, including an owner who is away.
func isProtectedTarget(roomID string, caller, target uint) (bool, error) {
	if caller == target {
		return true, nil
	}
	row, err := roleRow(database.DB, roomID, target)
	return row != nil && row.Role == models.RoleOwner, err
}

// loadRoom loads the room named in the path, writing the error response
// itself.
func loadRoom(c *gin.Context) (models.Room, bool) {
	var room models.Room
	if err := database.DB.Where("room_id = ?", c.Param("room_id")).First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			apierror.Respond(c, http.StatusNotFound, apierror.RoomNotFound, "Oda bulunamadı")
		} else {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		}
		return room, false
	}
	return room, true
}

// requireAccess loads the room named in the path and the caller's access,
// and checks it with allowed. It writes the error response itself and
// reports whether to continue.
func requireAccess(c *gin.Context, allowed func(access) bool, allowClosed bool) (models.Room, access, bool) {
	room, ok := loadRoom(c)
	if !ok {
		return room, access{}, false
	}
	caller, err := roomAccess(room.RoomID, c.MustGet("user").(models.User).ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return room, caller, false
	}
	if !allowed(caller) {
		respondPermissionRequired(c)
		return room, caller, false
	}
	if !allowClosed && room.Status != models.RoomStatusActive {
		apierror.Respond(c, http.StatusConflict, apierror.RoomNotActive, "Oda aktif değil")
		return room, caller, false
	}
	return room, caller, true
}

// requirePermission is requireAccess for a single permission.
func requirePermission(c *gin.Context, permission string, allowClosed bool) (models.Room, access, bool) {
	return requireAccess(c, func(a access) bool { return a.has(permission) }, allowClosed)
}

func respondPermissionRequired(c *gin.Context) {
	apierror.Respond(c, http.StatusForbidden, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
}

// respondTargetProtected rejects acting on the owner or on yourself.
func respondTargetProtected(c *gin.Context) {
	apierror.Respond(c, http.StatusForbidden, apierror.TargetProtected, "Bu katılımcı üzerinde işlem yapma yetkiniz yok")
}

// connectedParticipants lists the room's LiveKit participants; a media room
// that no longer exists simply has nobody connected.
func connectedParticipants(ctx context.Context, svc liveKitRoomService, roomID string) ([]*livekit.ParticipantInfo, error) {
	list, err := svc.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: roomID})
	if isTwirpNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return list.Participants, nil
}

// sessionsOf returns the connected sessions of one user.
func sessionsOf(ps []*livekit.ParticipantInfo, userID uint) []*livekit.ParticipantInfo {
	var sessions []*livekit.ParticipantInfo
	for _, p := range ps {
		if id, err := participantUserID(p.Identity); err == nil && id == userID {
			sessions = append(sessions, p)
		}
	}
	return sessions
}

// writeOwner makes a user the room owner: real when grantedBy chose them,
// acting when succession did (previous are the permissions to restore).
// It reports whether the user's microphone lock was lifted, since nobody may
// lock the owner.
func writeOwner(db *gorm.DB, roomID string, userID, grantedBy uint, acting bool, previous string) (unlocked bool, err error) {
	row, err := roleRow(db, roomID, userID)
	if err != nil {
		return false, err
	}
	if row == nil {
		row = &models.RoomRole{RoomID: roomID, UserID: userID}
	}
	row.Role, row.Permissions, row.GrantedAt, row.GrantedBy = models.RoleOwner, "", time.Now(), grantedBy
	row.Suspended, row.Acting, row.PreviousPermissions = false, acting, previous
	if err = db.Save(row).Error; err != nil {
		return false, err
	}
	result := db.Model(&models.RoomMicrophoneLock{}).Where("room_id = ? AND user_id = ? AND locked = ?", roomID, userID, true).Update("locked", false)
	return result.RowsAffected > 0, result.Error
}

// writePermissions makes a user a participant with permissions; without any
// the row is removed. GrantedAt is kept while the user keeps some permission,
// so succession's "granted earliest" survives later changes.
func writePermissions(db *gorm.DB, roomID string, userID uint, permissions []string, grantedBy uint) error {
	row, err := roleRow(db, roomID, userID)
	if err != nil {
		return err
	}
	stored := models.JoinPermissions(permissions)
	if stored == "" {
		if row != nil {
			return db.Delete(row).Error
		}
		return nil
	}
	if row == nil {
		row = &models.RoomRole{RoomID: roomID, UserID: userID}
	}
	if row.Role != models.RoleParticipant || row.Permissions == "" || row.GrantedAt.IsZero() {
		row.GrantedAt = time.Now()
	}
	row.Role, row.Permissions, row.GrantedBy = models.RoleParticipant, stored, grantedBy
	row.Suspended, row.Acting, row.PreviousPermissions = false, false, ""
	return db.Save(row).Error
}

// syncAccess pushes a user's access to every connected session: the role and
// permission attributes, a lifted microphone lock, and, without
// share_screen, the end of their screen share.
func syncAccess(ctx context.Context, svc liveKitRoomService, room *models.Room, ps []*livekit.ParticipantInfo, userID uint, a access, unlocked bool) error {
	for _, p := range sessionsOf(ps, userID) {
		_, err := svc.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: room.RoomID, Identity: p.Identity, Attributes: a.attributes()})
		if err != nil && !isTwirpNotFound(err) {
			return err
		}
		if unlocked {
			if err := applyMicrophonePolicy(ctx, svc, *room, p, false, false); err != nil {
				return err
			}
		}
		if !a.has(models.PermissionShareScreen) && p.Identity == room.ScreenShareIdentity {
			if err := revokeScreenShare(ctx, svc, room.RoomID, p); err != nil {
				return err
			}
			if err := saveShare(room, "", 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// connectedOwnerExists reports whether a user other than except is the
// active owner and has a connected session.
func connectedOwnerExists(roomID string, ps []*livekit.ParticipantInfo, except uint) (bool, error) {
	var owners []models.RoomRole
	if err := database.DB.Where("room_id = ? AND role = ? AND suspended = ?", roomID, models.RoleOwner, false).Find(&owners).Error; err != nil {
		return false, err
	}
	for _, owner := range owners {
		if owner.UserID != except && len(sessionsOf(ps, owner.UserID)) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// roomParticipants connects to LiveKit and lists the room, writing the error
// response itself on failure.
func roomParticipants(c *gin.Context, room models.Room) (context.Context, liveKitRoomService, []*livekit.ParticipantInfo, bool) {
	ctx, svc, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return ctx, nil, nil, false
	}
	ps, err := connectedParticipants(ctx, svc, room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcılar sunucudan alınamadı")
		return ctx, nil, nil, false
	}
	return ctx, svc, ps, true
}

// SetParticipantPermissions replaces a participant's permissions. The owner
// and anyone with grant_permissions may grant any permission; nobody may
// change the owner's or their own. It applies even if the target is not
// connected.
func SetParticipantPermissions(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, _, ok := requirePermission(c, models.PermissionGrantPermissions, false)
	if !ok {
		return
	}
	var input struct {
		Permissions *[]string `json:"permissions"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Permissions == nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "İzin listesi gerekli")
		return
	}
	identity := c.Param("identity")
	targetID, err := participantUserID(identity)
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
		return
	}
	var users int64
	if err = database.DB.Model(&models.User{}).Where("id = ?", targetID).Count(&users).Error; err != nil || users == 0 {
		apierror.Respond(c, http.StatusNotFound, apierror.ParticipantNotFound, "Kullanıcı bulunamadı")
		return
	}
	caller := c.MustGet("user").(models.User)
	protected, err := isProtectedTarget(room.RoomID, caller.ID, targetID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	if protected {
		respondTargetProtected(c)
		return
	}
	ctx, svc, ps, ok := roomParticipants(c, room)
	if !ok {
		return
	}
	permissions := models.NormalizePermissions(*input.Permissions)
	if err = writePermissions(database.DB, room.RoomID, targetID, permissions, caller.ID); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İzinler kaydedilemedi")
		return
	}
	if err = syncAccess(ctx, svc, &room, ps, targetID, access{Role: models.RoleParticipant, Permissions: permissions}, false); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "İzinler kaydedildi fakat tüm oturumlara uygulanamadı. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"identity": identity, "permissions": permissions})
}

// TransferOwnership makes a connected participant the owner. The caller
// becomes a participant with every permission. A transfer is final: an owner
// who is away (when an acting owner transfers) loses the right to reclaim
// and also keeps every permission as a participant.
func TransferOwnership(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := loadRoom(c)
	if !ok {
		return
	}
	caller := c.MustGet("user").(models.User)
	callerAccess, err := roomAccess(room.RoomID, caller.ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	if callerAccess.Role != models.RoleOwner {
		apierror.Respond(c, http.StatusForbidden, apierror.OwnerRequired, "Bu işlem yalnızca oda sahibine açık")
		return
	}
	if room.Status != models.RoomStatusActive {
		apierror.Respond(c, http.StatusConflict, apierror.RoomNotActive, "Oda aktif değil")
		return
	}
	var input struct {
		Identity string `json:"identity"`
	}
	if c.ShouldBindJSON(&input) != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Katılımcı kimliği gerekli")
		return
	}
	targetID, err := participantUserID(input.Identity)
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
		return
	}
	if targetID == caller.ID {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Sahipliği kendinize devredemezsiniz")
		return
	}
	ctx, svc, ps, ok := roomParticipants(c, room)
	if !ok {
		return
	}
	connected := false
	for _, p := range ps {
		connected = connected || p.Identity == input.Identity
	}
	if !connected {
		apierror.Respond(c, http.StatusNotFound, apierror.ParticipantNotFound, "Katılımcı artık odada değil")
		return
	}
	var unlocked bool
	var away []models.RoomRole
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("room_id = ? AND role = ? AND suspended = ?", room.RoomID, models.RoleOwner, true).Find(&away).Error; err != nil {
			return err
		}
		for _, owner := range away {
			if err := writePermissions(tx, room.RoomID, owner.UserID, models.AllPermissions, caller.ID); err != nil {
				return err
			}
		}
		var err error
		if unlocked, err = writeOwner(tx, room.RoomID, targetID, caller.ID, false, ""); err != nil {
			return err
		}
		return writePermissions(tx, room.RoomID, caller.ID, models.AllPermissions, caller.ID)
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Sahiplik devredilemedi")
		return
	}
	everything := access{Role: models.RoleParticipant, Permissions: models.AllPermissions}
	err = syncAccess(ctx, svc, &room, ps, targetID, ownerAccess(), unlocked)
	if err == nil {
		err = syncAccess(ctx, svc, &room, ps, caller.ID, everything, false)
	}
	for _, owner := range away {
		if err == nil {
			err = syncAccess(ctx, svc, &room, ps, owner.UserID, everything, false)
		}
	}
	if err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Sahiplik devredildi fakat tüm oturumlara uygulanamadı. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"identity": input.Identity, "role": models.RoleOwner, "caller_role": models.RoleParticipant})
}
