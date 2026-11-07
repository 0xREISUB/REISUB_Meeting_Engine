# REISUB_Meeting_Engine
It is server side of REISUB_Meeting app

## Docker deployment

The Compose stack runs the Go backend and LiveKit. The backend stores SQLite data in the persistent `reisub-meeting-db` volume.

```sh
mkdir -p config
cp config/stack.env.example config/stack.env
chmod 600 config/stack.env
```

Edit `config/stack.env` before deployment. The included `devkey` and `devsecret` values are for development only; use unique credentials before production.

### Preserve an existing database

Before the first `docker compose up`, copy an existing `reisub.db` into the named volume. Run these commands from this directory; if there is no existing database file, skip the migration block.

```sh
docker volume create reisub-meeting-db
if [ -f reisub.db ]; then
	docker run --rm \
		-v reisub-meeting-db:/data \
		-v "$PWD/reisub.db:/migration/reisub.db:ro" \
		alpine:3.22 sh -c 'if [ ! -e /data/reisub.db ]; then cp /migration/reisub.db /data/reisub.db; fi && chown 10001:10001 /data /data/reisub.db && chmod 0750 /data && chmod 0600 /data/reisub.db'
else
	docker run --rm -v reisub-meeting-db:/data alpine:3.22 sh -c 'chown 10001:10001 /data && chmod 0750 /data'
fi
```

Build and start the stack:

```sh
docker compose --env-file config/stack.env up -d --build
docker compose --env-file config/stack.env ps
```

The Compose project name and LiveKit service name match the existing deployment so Compose can manage its current `livekit-server` container rather than creating a port-conflicting second instance. The backend listens on the configured `PORT` (8080 by default). LiveKit keeps the existing API/WebSocket and WebRTC media port mappings.

### LiveKit media networking

LiveKit runs with an explicit production configuration. Its API/WebSocket uses
port 7880, ICE/TCP uses 7881, and all ICE/UDP traffic uses the published UDP mux
port 7882. Allow TCP 7881 and UDP 7882 through the host and provider firewalls;
the WebSocket reverse proxy alone does not carry WebRTC media.

By default, `LIVEKIT_USE_EXTERNAL_IP=true` discovers the public media IP through
STUN. If discovery is unavailable or returns the wrong address, configure the
server's public IP in `config/stack.env` and disable discovery:

```dotenv
LIVEKIT_NODE_IP=203.0.113.10
LIVEKIT_USE_EXTERNAL_IP=false
```

Replace the example IP with the actual public IP. `LIVEKIT_PUBLIC_URL` is the
signaling URL; it does not override the IP advertised for media. Advertising an
unreachable Docker/private IP can cause ICE failures even while the app appears
to have joined the room.

After changing these settings, validate the configuration and recreate only
LiveKit. This interrupts active calls, so participants must rejoin:

```sh
docker compose --env-file config/stack.env config --quiet
docker compose --env-file config/stack.env up -d --no-deps livekit
```

See [LiveKit ports and firewall documentation](https://docs.livekit.io/transport/self-hosting/ports-firewall/).

### Day-to-day operations

Run these on the server from this directory:

```sh
docker compose --env-file config/stack.env ps                        # status
docker compose --env-file config/stack.env logs --tail=100 backend livekit   # logs
docker compose --env-file config/stack.env restart backend           # restart one service
docker compose --env-file config/stack.env up -d --build             # apply config or code changes
```

To change settings, edit `config/stack.env` and run the last command. Restarts
and container recreation keep the database volume. Do not use
`docker compose down -v` unless you intentionally want to delete the database.

## Room owner controls

The authenticated user who creates a room is its owner. Owner privileges are
checked by the backend against `Room.OwnerID`; clients receive no direct LiveKit
room-admin grant.

- `POST /rooms/:room_id/close` closes the room to new joins and deletes its LiveKit
  media session. Closing can be retried safely after a transport failure.
- `POST /rooms/:room_id/participants/:identity/microphone` accepts
  `{"locked":true}` to mute and lock the microphone. The policy applies to the
  user's current sessions and persists in SQLite for subsequent joins.
- Send `{"locked":false}` to unlock, or
  `{"locked":false,"request_unmute":true}` to unlock and ask the participant to
  enable their microphone. The client must accept the request; remote unmute is
  not enabled.

Microphone locks revoke the microphone publishing source while preserving other
media permissions. LiveKit participant attributes deliver locks and requests to
clients, and participants cannot modify those attributes themselves.

Deploy the backend and LiveKit Compose configuration together for this feature.
The backend now explicitly creates rooms, and `room.auto_create: false` prevents
an old room token from recreating a closed room. `LIVEKIT_INTERNAL_URL` is set to
`http://livekit:7880` in Compose for authenticated server-to-server calls; outside
Compose it falls back to `LIVEKIT_PUBLIC_URL` (converting WS/WSS to HTTP/HTTPS).
SQLite tables are migrated automatically at backend startup.

New join tokens expire after five minutes; active sessions are refreshed by
LiveKit. On self-hosted LiveKit, existing tokens cannot be revoked immediately.
An old token carrying earlier microphone permissions can therefore remain valid
until it expires; freshly requested join tokens always apply the stored lock.

### Screen sharing and meeting settings

`allow_participant_screen_share` defaults to `false` for new rooms. The owner
may always request to share; other participants may share only when the owner
enables the setting. Turning it off again stops the current participant's share
from the server. Only one session per room, the owner included, may share at a
time.

Authenticated API:

- `GET /rooms/:room_id/state`: the sharing setting and `screen_share_identity`.
- `POST /rooms/:room_id/settings`: owner only,
  `{"allow_participant_screen_share": true|false}`.
- `POST /rooms/:room_id/screen-share/start`: `{"identity":"user-ID-UUID"}`;
  only a connected session of the requesting user is granted. Returns `409` if
  another session holds the slot.
- `POST /rooms/:room_id/screen-share/stop`: stops the share for that identity
  (the holder or the room owner).

All four return `{"allow_participant_screen_share": bool, "screen_share_identity": string}`.

Join JWTs only allow the camera and, unless locked, the microphone. Screen and
screen-audio sources are granted only to the selected connected session through
LiveKit `UpdateParticipant`. Whenever that grant is revoked (setting turned off,
owner stops the share, the slot moves to another session, or a stale
reservation is released) the backend also server-mutes the participant's
`SCREEN_SHARE` and `SCREEN_SHARE_AUDIO` tracks, so a client that ignores the
permission change cannot keep streaming. Microphone lock/unlock preserves the
screen restriction.

A per-room lock serializes joins, closure, microphone moderation and sharing
changes within one backend process; the current deployment runs a single backend
instance. A distributed lock or atomic reservation is required before running
several backend instances.

A lost session's reservation is cleared on the next state or start request. A
new holder has 30 seconds to start publishing; afterwards the slot is held only
while an unmuted `SCREEN_SHARE` video track exists (screen audio alone does not
hold it). Clients poll the state every two seconds. The state endpoint contacts
LiveKit only while someone holds the slot, asks about the holder alone, and
reuses a confirmation for one poll interval, so a vanished holder is reported at
most about two seconds late.

Deploy the Engine before the new Linux/Android clients. JWTs issued by an older
version may carry their earlier permissions for at most their five-minute
lifetime; participants should leave and rejoin after a deployment.

The client gives the screen share priority in the main view. On desktop a
full-screen button hides the participant strip. Shares can be zoomed 1–6× with
the mouse wheel or a two-finger pinch on phones, and reset. The Android client
starts a `mediaProjection` foreground service after the user grants screen
capture permission, following the
[LiveKit Flutter screen sharing flow](https://github.com/livekit/client-sdk-flutter#screen-sharing).
The service stops when sharing ends or the user leaves the meeting.

## Accounts

`POST /register` and `POST /login` take `{"nick", "password"}`. The nick is
trimmed of leading/trailing whitespace before it is stored or looked up and must
not be blank; the password is used verbatim and may be at most 72 bytes. At
startup the backend logs a warning if any stored nick still has surrounding
whitespace (such accounts cannot log in with trimmed input and need a manual
migration).

## Errors

Every error response is JSON:

```json
{"error": "Başka bir katılımcı ekranını paylaşıyor", "code": "screen_share_busy"}
```

`error` is a human-readable (Turkish) message that clients may show verbatim.
`code` is stable and meant for programs: branch on it or localize from it. Codes
are never renamed; new ones may be added. A `401` from any `/rooms` route means
the session token is missing or expired.

| Code | Meaning |
| --- | --- |
| `invalid_request` | Body missing or malformed |
| `not_found` | Unknown route |
| `internal_error` | Unexpected server failure (usually the database) |
| `missing_credentials` | Nick or password missing, or nick blank after trimming |
| `password_too_long` | Password longer than 72 bytes |
| `nick_taken` | Nick already registered |
| `user_not_found` | No account with this nick |
| `wrong_password` | Password does not match |
| `auth_required` | No session token sent (401) |
| `invalid_token` | Session token unknown or expired (401 on `/rooms`) |
| `name_required` | Display name missing |
| `invalid_room_request` | Display name or `NNN-NNN-NNN` room code missing |
| `room_not_found` | Room does not exist or is not active |
| `room_not_active` | Room is closing or closed |
| `not_room_owner` | Only the room owner may do this |
| `invalid_identity` | Malformed participant identity |
| `identity_not_owned` | The identity belongs to another user |
| `participant_not_found` | Identity not connected to the media room |
| `media_server_not_configured` | LiveKit credentials or URL missing |
| `media_server_error` | A LiveKit call failed; retry |
| `screen_share_not_allowed` | Owner has not allowed participants to share |
| `screen_share_busy` | Another session holds the screen-share slot |
| `publish_not_permitted` | Session may not publish media |
| `invalid_message` | Chat message empty or over 1000 characters |

## Development

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

CI runs the same checks plus `go mod tidy -diff` on every push and pull request.
