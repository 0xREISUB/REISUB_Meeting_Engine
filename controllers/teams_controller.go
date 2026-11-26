package controllers

import (
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	maxTeamNameRunes = 40
	maxTeamMembers   = 50
)

var teamColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// defaultTeamColors are given to teams created without a colour, in turn.
var defaultTeamColors = []string{"#7C4DFF", "#009688", "#FF9800", "#E91E63", "#2196F3", "#4CAF50"}

// teamSummary is a team as its members see it in their list.
type teamSummary struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Role        string `json:"role"`
	MemberCount int    `json:"member_count"`
	OnlineCount int    `json:"online_count"`
	// MeetingLive is whether someone is in the team's meeting room now.
	MeetingLive bool   `json:"meeting_live"`
	RoomID      string `json:"room_id,omitempty"`
	// Unread counts the team chat's messages the user has not read.
	Unread int64 `json:"unread"`
}

type teamMemberResponse struct {
	User userSummary `json:"user"`
	Role string      `json:"role"`
}

type teamDetail struct {
	teamSummary
	Members []teamMemberResponse `json:"members"`
}

// teamSummaries describes the user's teams, given their memberships.
func teamSummaries(memberships []models.TeamMember, now time.Time) ([]teamSummary, error) {
	result := []teamSummary{}
	if len(memberships) == 0 {
		return result, nil
	}
	teamIDs := make([]uint, len(memberships))
	roles := map[uint]string{}
	for i, m := range memberships {
		teamIDs[i] = m.TeamID
		roles[m.TeamID] = m.Role
	}
	var teams []models.Team
	if err := database.DB.Where("id IN ?", teamIDs).Order("name").Find(&teams).Error; err != nil {
		return nil, err
	}
	var members []models.TeamMember
	if err := database.DB.Where("team_id IN ?", teamIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	userIDs := make([]uint, 0, len(members))
	for _, m := range members {
		userIDs = append(userIDs, m.UserID)
	}
	users, err := usersByID(userIDs)
	if err != nil {
		return nil, err
	}
	inMeeting, err := usersInMeeting(userIDs)
	if err != nil {
		return nil, err
	}
	roomIDs := make([]string, 0, len(teams))
	for _, team := range teams {
		if team.RoomID != "" {
			roomIDs = append(roomIDs, team.RoomID)
		}
	}
	live, err := roomsInUse(roomIDs)
	if err != nil {
		return nil, err
	}
	teamConversations := make([]string, 0, len(teams))
	for _, team := range teams {
		teamConversations = append(teamConversations, teamConversation(team.ID))
	}
	unread, err := unreadCounts(memberships[0].UserID, teamConversations)
	if err != nil {
		return nil, err
	}
	for _, team := range teams {
		summary := teamSummary{
			ID:          team.ID,
			Name:        team.Name,
			Color:       team.Color,
			Role:        roles[team.ID],
			MeetingLive: team.RoomID != "" && live[team.RoomID],
			Unread:      unread[teamConversation(team.ID)],
		}
		if summary.MeetingLive {
			summary.RoomID = team.RoomID
		}
		for _, m := range members {
			if m.TeamID != team.ID {
				continue
			}
			summary.MemberCount++
			if user, ok := users[m.UserID]; ok && visiblePresence(user, inMeeting[m.UserID], now) != presenceOffline {
				summary.OnlineCount++
			}
		}
		result = append(result, summary)
	}
	return result, nil
}

func usersByID(ids []uint) (map[uint]models.User, error) {
	result := map[uint]models.User{}
	if len(ids) == 0 {
		return result, nil
	}
	var users []models.User
	if err := database.DB.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		result[user.ID] = user
	}
	return result, nil
}

type teamInput struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// CreateTeam starts a team with the user as its owner.
func CreateTeam(c *gin.Context) {
	var input teamInput
	_ = c.ShouldBindJSON(&input)
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > maxTeamNameRunes {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidTeam, "Takım adı 1-40 karakter olmalı")
		return
	}
	if input.Color != "" && !teamColorPattern.MatchString(input.Color) {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidTeam, "Geçersiz takım rengi")
		return
	}
	user := c.MustGet("user").(models.User)
	team := models.Team{Name: name, Color: strings.ToUpper(input.Color), OwnerID: user.ID}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&team).Error; err != nil {
			return err
		}
		if team.Color == "" {
			team.Color = defaultTeamColors[int(team.ID)%len(defaultTeamColors)]
			if err := tx.Model(&team).Update("color", team.Color).Error; err != nil {
				return err
			}
		}
		return tx.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID, Role: models.TeamRoleOwner, JoinedAt: time.Now()}).Error
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım oluşturulamadı")
		return
	}
	summaries, err := teamSummaries([]models.TeamMember{{TeamID: team.ID, UserID: user.ID, Role: models.TeamRoleOwner}}, time.Now())
	if err != nil || len(summaries) == 0 {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	socialChanged(user.ID)
	c.JSON(http.StatusCreated, summaries[0])
}

// memberTeam loads :team_id for a member of it. It writes the error
// response itself; non-members are told the team does not exist.
func memberTeam(c *gin.Context, userID uint) (models.Team, models.TeamMember, bool) {
	id, err := strconv.ParseUint(c.Param("team_id"), 10, 64)
	if err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.TeamNotFound, "Takım bulunamadı")
		return models.Team{}, models.TeamMember{}, false
	}
	membership, err := teamMembership(uint(id), userID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return models.Team{}, models.TeamMember{}, false
	}
	var teams []models.Team
	if membership != nil {
		err = database.DB.Where("id = ?", id).Limit(1).Find(&teams).Error
	}
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return models.Team{}, models.TeamMember{}, false
	}
	if membership == nil || len(teams) == 0 {
		apierror.Respond(c, http.StatusNotFound, apierror.TeamNotFound, "Takım bulunamadı")
		return models.Team{}, models.TeamMember{}, false
	}
	return teams[0], *membership, true
}

// GetTeam answers a team with its members.
func GetTeam(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	team, membership, ok := memberTeam(c, user.ID)
	if !ok {
		return
	}
	now := time.Now()
	summaries, err := teamSummaries([]models.TeamMember{membership}, now)
	if err != nil || len(summaries) == 0 {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	var members []models.TeamMember
	if err := database.DB.Where("team_id = ?", team.ID).Order("joined_at").Find(&members).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	ids := make([]uint, len(members))
	for i, m := range members {
		ids[i] = m.UserID
	}
	users, err := usersByID(ids)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	inMeeting, err := usersInMeeting(ids)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	detail := teamDetail{teamSummary: summaries[0], Members: []teamMemberResponse{}}
	for _, m := range members {
		if member, ok := users[m.UserID]; ok {
			detail.Members = append(detail.Members, teamMemberResponse{User: summarize(member, inMeeting, now), Role: m.Role})
		}
	}
	c.JSON(http.StatusOK, detail)
}

func managesTeam(role string) bool {
	return role == models.TeamRoleOwner || role == models.TeamRoleAdmin
}

type teamMemberInput struct {
	UserID uint `json:"user_id" binding:"required"`
}

// AddTeamMember adds one of the adder's friends; owners and admins may.
func AddTeamMember(c *gin.Context) {
	var input teamMemberInput
	if err := c.ShouldBindJSON(&input); err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Kullanıcı gerekli")
		return
	}
	user := c.MustGet("user").(models.User)
	team, membership, ok := memberTeam(c, user.ID)
	if !ok {
		return
	}
	if !managesTeam(membership.Role) {
		apierror.Respond(c, http.StatusForbidden, apierror.TeamRoleRequired, "Bu işlem için takım yöneticisi olmalısınız")
		return
	}
	friends, err := areFriends(user.ID, input.UserID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlık okunamadı")
		return
	}
	if !friends {
		apierror.Respond(c, http.StatusBadRequest, apierror.NotFriends, "Takıma yalnızca arkadaşlarınızı ekleyebilirsiniz")
		return
	}
	var count int64
	if err := database.DB.Model(&models.TeamMember{}).Where("team_id = ?", team.ID).Count(&count).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	if count >= maxTeamMembers {
		apierror.Respond(c, http.StatusConflict, apierror.TeamFull, "Takım en fazla 50 kişi olabilir")
		return
	}
	existing, err := teamMembership(team.ID, input.UserID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	if existing != nil {
		apierror.Respond(c, http.StatusConflict, apierror.AlreadyTeamMember, "Bu kişi zaten takımda")
		return
	}
	if err := database.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: input.UserID, Role: models.TeamRoleMember, JoinedAt: time.Now()}).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Üye eklenemedi")
		return
	}
	socialChanged(teamMemberIDs(team.ID)...)
	c.Status(http.StatusNoContent)
}

// RemoveTeamMember takes someone out of the team, or lets the user leave
// it. Admins may remove members, the owner anyone. An owner who leaves
// hands the team to the longest-serving admin, else member; the last one
// out deletes the team.
func RemoveTeamMember(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	team, membership, ok := memberTeam(c, user.ID)
	if !ok {
		return
	}
	targetID, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.UserNotFound, "Kullanıcı bulunamadı")
		return
	}
	target, err := teamMembership(team.ID, uint(targetID))
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	if target == nil {
		apierror.Respond(c, http.StatusNotFound, apierror.UserNotFound, "Kullanıcı takımda değil")
		return
	}
	before := teamMemberIDs(team.ID)
	leaving := target.UserID == user.ID
	allowed := leaving ||
		membership.Role == models.TeamRoleOwner ||
		(membership.Role == models.TeamRoleAdmin && target.Role == models.TeamRoleMember)
	if !allowed {
		apierror.Respond(c, http.StatusForbidden, apierror.TeamRoleRequired, "Bu işlem için yetkiniz yok")
		return
	}
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(target).Error; err != nil {
			return err
		}
		if target.Role != models.TeamRoleOwner {
			return nil
		}
		var next []models.TeamMember
		if err := tx.Where("team_id = ?", team.ID).
			Order("CASE role WHEN 'admin' THEN 0 ELSE 1 END, joined_at, id").Limit(1).Find(&next).Error; err != nil {
			return err
		}
		if len(next) == 0 {
			return deleteTeamTx(tx, team.ID)
		}
		if err := tx.Model(&next[0]).Update("role", models.TeamRoleOwner).Error; err != nil {
			return err
		}
		return tx.Model(&team).Update("owner_id", next[0].UserID).Error
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Üye çıkarılamadı")
		return
	}
	socialChanged(before...)
	c.Status(http.StatusNoContent)
}

type teamRoleInput struct {
	Role string `json:"role" binding:"required"`
}

// SetTeamMemberRole makes a member an admin or a plain member again; only
// the owner may.
func SetTeamMemberRole(c *gin.Context) {
	var input teamRoleInput
	if err := c.ShouldBindJSON(&input); err != nil || (input.Role != models.TeamRoleAdmin && input.Role != models.TeamRoleMember) {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Geçersiz rol")
		return
	}
	user := c.MustGet("user").(models.User)
	team, membership, ok := memberTeam(c, user.ID)
	if !ok {
		return
	}
	if membership.Role != models.TeamRoleOwner {
		apierror.Respond(c, http.StatusForbidden, apierror.TeamRoleRequired, "Bu işlem için takım sahibi olmalısınız")
		return
	}
	targetID, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil || uint(targetID) == user.ID {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Geçersiz kullanıcı")
		return
	}
	result := database.DB.Model(&models.TeamMember{}).Where("team_id = ? AND user_id = ?", team.ID, targetID).Update("role", input.Role)
	if result.Error != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Rol değiştirilemedi")
		return
	}
	if result.RowsAffected == 0 {
		apierror.Respond(c, http.StatusNotFound, apierror.UserNotFound, "Kullanıcı takımda değil")
		return
	}
	socialChanged(teamMemberIDs(team.ID)...)
	c.Status(http.StatusNoContent)
}

// DeleteTeam ends the team, its memberships and its chat; only the owner
// may.
func DeleteTeam(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	team, membership, ok := memberTeam(c, user.ID)
	if !ok {
		return
	}
	if membership.Role != models.TeamRoleOwner {
		apierror.Respond(c, http.StatusForbidden, apierror.TeamRoleRequired, "Bu işlem için takım sahibi olmalısınız")
		return
	}
	members := teamMemberIDs(team.ID)
	if err := database.DB.Transaction(func(tx *gorm.DB) error { return deleteTeamTx(tx, team.ID) }); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım silinemedi")
		return
	}
	socialChanged(members...)
	c.Status(http.StatusNoContent)
}

func deleteTeamTx(tx *gorm.DB, teamID uint) error {
	if err := tx.Where("team_id = ?", teamID).Delete(&models.TeamMember{}).Error; err != nil {
		return err
	}
	if err := tx.Where("conversation = ?", teamConversation(teamID)).Delete(&models.SocialMessage{}).Error; err != nil {
		return err
	}
	return tx.Delete(&models.Team{}, teamID).Error
}

// teamMeetingLocks keeps two members starting the same team's meeting at
// once from opening two rooms.
var teamMeetingLocks sync.Map

// StartTeamMeeting answers the room of the team's meeting: the one going
// on, or a new one, announced in the team's chat.
func StartTeamMeeting(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	team, _, ok := memberTeam(c, user.ID)
	if !ok {
		return
	}
	lock, _ := teamMeetingLocks.LoadOrStore(team.ID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	// Read again: another member may just have started it.
	if err := database.DB.First(&team, team.ID).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
		return
	}
	roomID, err := reusableTeamRoom(team)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	live := false
	if roomID != "" {
		inUse, err := roomsInUse([]string{roomID})
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
			return
		}
		live = inUse[roomID]
	} else {
		room, ok := openRoom(c, user, user.Name())
		if !ok {
			return
		}
		roomID = room.RoomID
		if err := database.DB.Model(&team).Update("room_id", roomID).Error; err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım güncellenemedi")
			return
		}
	}
	// Nobody there yet: the meeting starts now, and the team hears of it.
	if !live {
		invited, err := inviteToMeeting(teamConversation(team.ID), user.ID, roomID)
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Davet gönderilemedi")
			return
		}
		// The team's phones ring.
		if invited {
			place := callPlace{kind: "team", targetID: team.ID, title: team.Name}
			if err := startCall(user, roomID, teamConversation(team.ID), place, teamMemberIDs(team.ID)); err != nil {
				log.Printf("warning: could not ring team %d: %v", team.ID, err)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"room_id": roomID, "started": !live})
}

// reusableTeamRoom answers the team's room when it can still be joined;
// otherwise "", and a new room is opened.
func reusableTeamRoom(team models.Team) (string, error) {
	if team.RoomID == "" {
		return "", nil
	}
	var rooms []models.Room
	if err := database.DB.Where("room_id = ? AND status = ?", team.RoomID, models.RoomStatusActive).Limit(1).Find(&rooms).Error; err != nil {
		return "", err
	}
	if len(rooms) == 0 || pastLimit(rooms[0]) {
		return "", nil
	}
	return team.RoomID, nil
}

// inviteRepeatWindow keeps a meeting started twice in a row from inviting
// everyone twice.
const inviteRepeatWindow = 2 * time.Minute

// inviteToMeeting leaves a call message for the room in the conversation,
// unless one was left there lately.
// It answers whether it invited now; a repeat within inviteRepeatWindow
// does not.
func inviteToMeeting(conversation string, senderID uint, roomID string) (bool, error) {
	var recent int64
	if err := database.DB.Model(&models.SocialMessage{}).
		Where("conversation = ? AND room_id = ? AND created_at > ?", conversation, roomID, time.Now().Add(-inviteRepeatWindow)).
		Count(&recent).Error; err != nil {
		return false, err
	}
	if recent > 0 {
		return false, nil
	}
	invite := models.SocialMessage{Conversation: conversation, SenderID: senderID, RoomID: roomID}
	if err := database.DB.Create(&invite).Error; err != nil {
		return false, err
	}
	publishMessage(invite)
	return true, nil
}
