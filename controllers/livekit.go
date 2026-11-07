package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
)

// liveKitRoomService is the subset of the LiveKit RoomService API the backend
// uses. Tests replace newLiveKitRoomService with a fake.
type liveKitRoomService interface {
	CreateRoom(context.Context, *livekit.CreateRoomRequest) (*livekit.Room, error)
	DeleteRoom(context.Context, *livekit.DeleteRoomRequest) (*livekit.DeleteRoomResponse, error)
	ListParticipants(context.Context, *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error)
	GetParticipant(context.Context, *livekit.RoomParticipantIdentity) (*livekit.ParticipantInfo, error)
	UpdateParticipant(context.Context, *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error)
	MutePublishedTrack(context.Context, *livekit.MuteRoomTrackRequest) (*livekit.MuteRoomTrackResponse, error)
}

var newLiveKitRoomService = func(endpoint string) liveKitRoomService {
	return livekit.NewRoomServiceProtobufClient(endpoint, &http.Client{Timeout: 10 * time.Second})
}

// connectLiveKit returns a RoomService client and a context carrying a
// short-lived server-side admin token scoped to roomID.
func connectLiveKit(ctx context.Context, roomID string) (context.Context, liveKitRoomService, error) {
	key, secret := os.Getenv("LIVEKIT_API_KEY"), os.Getenv("LIVEKIT_API_SECRET")
	if key == "" || secret == "" {
		return ctx, nil, errors.New("LiveKit credentials missing")
	}
	endpoint := os.Getenv("LIVEKIT_INTERNAL_URL")
	if endpoint == "" {
		endpoint = liveKitURL()
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ctx, nil, err
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ctx, nil, errors.New("invalid LiveKit service URL")
	}
	token, err := auth.NewAccessToken(key, secret).SetValidFor(time.Minute).
		SetVideoGrant(&auth.VideoGrant{Room: roomID, RoomAdmin: true, RoomCreate: true}).ToJWT()
	if err != nil {
		return ctx, nil, err
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	ctx, err = twirp.WithHTTPRequestHeaders(ctx, headers)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, newLiveKitRoomService(strings.TrimRight(parsed.String(), "/")), nil
}

// liveKitURL is the signaling URL handed to clients.
func liveKitURL() string {
	if value := os.Getenv("LIVEKIT_PUBLIC_URL"); value != "" {
		return value
	}
	return "ws://localhost:7880"
}

// isTwirpNotFound reports whether a LiveKit call failed because the target
// (room, participant or track) no longer exists.
func isTwirpNotFound(err error) bool {
	var rpcErr twirp.Error
	return errors.As(err, &rpcErr) && rpcErr.Code() == twirp.NotFound
}
