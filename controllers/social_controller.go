package controllers

import (
	"net/http"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

type friendRequestResponse struct {
	ID   uint        `json:"id"`
	User userSummary `json:"user"`
}

type socialOverview struct {
	Profile  profileResponse         `json:"profile"`
	Friends  []userSummary           `json:"friends"`
	Requests []friendRequestResponse `json:"requests"`
	// Outgoing are the requests the user sent that wait for an answer.
	Outgoing []friendRequestResponse `json:"outgoing"`
	Teams    []teamSummary           `json:"teams"`
}

// GetSocialOverview answers everything the social tab shows at once:
// the profile, friends and whether they are around, friend requests both
// ways, and teams. Clients ask for it every few seconds while it is open,
// which also keeps the user online for their friends.
func GetSocialOverview(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	now := time.Now()
	var rows []models.Friendship
	if err := database.DB.Where("user_id = ? OR friend_id = ?", user.ID, user.ID).Order("id").Find(&rows).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlar okunamadı")
		return
	}
	otherIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		otherIDs = append(otherIDs, otherSide(row, user.ID))
	}
	others, err := usersByID(otherIDs)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlar okunamadı")
		return
	}
	inMeeting, err := usersInMeeting(otherIDs)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlar okunamadı")
		return
	}
	overview := socialOverview{
		Profile:  makeProfile(user),
		Friends:  []userSummary{},
		Requests: []friendRequestResponse{},
		Outgoing: []friendRequestResponse{},
	}
	for _, row := range rows {
		other, ok := others[otherSide(row, user.ID)]
		if !ok {
			continue
		}
		summary := summarize(other, inMeeting, now)
		switch {
		case row.Status == models.FriendshipAccepted:
			overview.Friends = append(overview.Friends, summary)
		case row.FriendID == user.ID:
			overview.Requests = append(overview.Requests, friendRequestResponse{ID: row.ID, User: summary})
		default:
			overview.Outgoing = append(overview.Outgoing, friendRequestResponse{ID: row.ID, User: summary})
		}
	}
	var memberships []models.TeamMember
	if err := database.DB.Where("user_id = ?", user.ID).Find(&memberships).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takımlar okunamadı")
		return
	}
	if overview.Teams, err = teamSummaries(memberships, now); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takımlar okunamadı")
		return
	}
	c.JSON(http.StatusOK, overview)
}

func otherSide(row models.Friendship, userID uint) uint {
	if row.UserID == userID {
		return row.FriendID
	}
	return row.UserID
}
