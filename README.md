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

The Compose project name and LiveKit service name match the existing deployment so Compose can manage its current `livekit-server` container rather than creating a port-conflicting second instance. The backend listens on the configured `PORT` (8080 by default) inside Docker
only; it publishes no host port. Nginx Proxy Manager, on the external
`proxy-agi` network, forwards `meeting.reisub.xyz` and `api.meeting.reisub.xyz`
to `reisub-meeting-backend:8080` with "Force SSL" on, so plain HTTP is
redirected to HTTPS. The backend sends `Strict-Transport-Security` on HTTPS
responses with `HSTS_MAX_AGE` seconds (Compose default 300, no
`includeSubDomains`/`preload`); leave NPM's own HSTS switch off, as it
hard-codes two years plus `preload`. To reach the API from the host itself,
use `docker compose exec backend wget -qO- http://127.0.0.1:8080/ping`. LiveKit keeps the existing API/WebSocket and WebRTC media port mappings.

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

LiveKit warns at startup when the host's UDP socket buffers are too small. Raise
them once on the host (persistent across reboots), then recreate LiveKit:

```sh
printf 'net.core.rmem_max = 5000000\nnet.core.wmem_max = 5000000\n' > /etc/sysctl.d/90-livekit-udp.conf
sysctl --system
```

### Logs

Container logs rotate at 5 × 50 MB per container (`x-logging` in
`docker-compose.yml`), so a verbose level cannot fill the disk. To debug media
problems (ICE, PLI/keyframes, dynacast), set `LIVEKIT_LOG_LEVEL=debug` in
`config/stack.env` and recreate LiveKit; switch back by setting it to `info`
(or removing the line) and recreating LiveKit again. Both recreations interrupt
active calls.

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

## Web app and invite links

The backend serves the Flutter web client on the same origin as the API, so
the web app uses its own origin as the meeting server and needs no CORS or
`--dart-define`. In production `https://meeting.reisub.xyz` (Nginx Proxy
Manager, forwarding to `reisub-meeting-backend:8080`) serves both; `https://api.meeting.reisub.xyz`
keeps serving the same API for the mobile apps.

- `WEB_DIR` (set to `/web` in Compose, mounted read-only from `./web`) holds the
  output of `flutter build web --release`. Files are served only for GET/HEAD
  requests that no API route matches, so the API is never shadowed. Without
  `WEB_DIR` the backend serves no web files.
- Caching: everything is `Cache-Control: no-cache` (revalidated on each load)
  except `assets/` and `canvaskit/` (`public, max-age=3600`).
- `GET /j/<room_id>` serves `invite/index.html` without authentication. The
  page reads the code from the path and offers to join in the browser
  (`/?room=<room_id>`) or download the app.
- `PUBLIC_WEB_URL` (e.g. `https://meeting.reisub.xyz`) adds
  `"invite_url": "<PUBLIC_WEB_URL>/j/<room_id>"` to `POST /rooms` and
  `POST /rooms/join`. Without it the field is omitted.

- `GET /.well-known/assetlinks.json` (no auth) proves to Android that the app
  may open this host's `/j/...` and `/invite` links directly. It lists
  `ANDROID_PACKAGE_NAME` (default `xyz.reisub.meeting`) and
  `ANDROID_CERT_FINGERPRINTS` (comma-separated SHA-256 signing certificate
  fingerprints; default: the release key of the official app). A fork signed
  with another key must set both. Android checks the host of the invite links,
  i.e. the host of `PUBLIC_WEB_URL`.

Browsers allow camera and microphone only on HTTPS origins, so always use the
TLS host.

### Deploying the web build

From this repository, with the client repository next to it and Flutter
installed locally:

```sh
scripts/deploy-web.sh vds            # ssh host; optional 2nd arg: client repo path
```

The script builds the client's `origin/main` in a temporary worktree (your
client working tree is not touched) with the client's own
`python3 tool/build_web.py`. That tool runs `flutter build web --release` and
then fills the invite page's server, download and project links from the
client's committed `app.env` (`REISUB_*` environment variables override it; the
client's local `.env` is not used). A plain `flutter build web` leaves those
placeholders empty and the invite page hides the links, so the script refuses
to upload a page with unfilled placeholders. It then uploads `build/web/` to
`/root/projects/REISUB_Meeting_Engine/web` with rsync, replacing the old build
only after every new file has arrived. No
container restart is needed. Building on the server is avoided on purpose:
Flutter builds are slow there and need the SDK.

## Room roles

Every room has per-user roles, stored in `room_roles` and checked by the
backend; clients never receive a LiveKit room-admin grant. Roles follow the
user ID, so reconnecting or joining from another device keeps them.

| Role | Can |
| --- | --- |
| `owner` | everything a moderator can, change anyone's role, transfer ownership, moderate moderators. A room may have several owners. |
| `moderator` | lock/unlock microphones and request unmute, change the participant screen-share setting, share their own screen regardless of that setting, stop another participant's share, end the meeting |
| `participant` | nothing privileged (no row in `room_roles`) |

Nobody can lock, request, or stop the share of an owner, and moderators cannot
act on other moderators. The user who creates a room is its first owner;
`Room.OwnerID` only records the creator and is not used for authorization.

Each participant's role is published as the LiveKit participant attribute
`meeting.role` (`"owner"`, `"moderator"`, or empty) in the join token and on
every change, so clients update live.

- `POST /rooms/:room_id/participants/:identity/role` `{"role":"owner"|"moderator"|"participant"}`
  (owners only). An owner may step down only while another owner is connected.
- `POST /rooms/:room_id/transfer-ownership` `{"identity":"..."}` (owners only):
  the connected target becomes owner, the caller becomes moderator.
- `POST /rooms/:room_id/leave`: the caller is leaving on purpose.
- `POST /rooms/:room_id/close` (owner or moderator) closes the room to new joins
  and deletes its LiveKit media session. Closing can be retried safely.
- `POST /rooms/:room_id/participants/:identity/microphone` (owner or moderator)
  accepts `{"locked":true}` to mute and lock the microphone, `{"locked":false}`
  to unlock, or `{"locked":false,"request_unmute":true}` to unlock and ask the
  participant to enable it. Locks persist for later joins; remote unmute is not
  enabled. Becoming an owner lifts a lock.

### Owner succession

When no owner remains connected (after `POST /leave`, or when LiveKit reports
`participant_left`/`room_finished` through the webhook):

1. the connected moderator who has been a moderator longest, otherwise
2. a uniformly random connected participant becomes an **acting owner**;
3. if nobody is connected, the next user to call `POST /rooms/join` does.

Owners who were away are suspended, not demoted. When any of them rejoins they
are owner again and every acting owner returns to their previous role. An acting
owner who leaves simply reverts. An explicit role change by an owner makes a role
permanent and ends any reclaim right. Succession is idempotent, so `/leave` and
the webhook may both arrive.

LiveKit posts its events to `http://backend:$PORT/livekit/webhook`
(configured in `docker-compose.yml`), signed with the LiveKit API key and
secret; unsigned or tampered requests are rejected with `401`.

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

`allow_participant_screen_share` defaults to `false` for new rooms. Owners and
moderators may always request to share; participants may share only when an
owner or moderator enables the setting. Turning it off again stops a
participant's current share from the server (an owner's or moderator's share
continues). Only one session per room may share at a time.

Authenticated API:

- `GET /rooms/:room_id/state`: the sharing setting and `screen_share_identity`.
- `POST /rooms/:room_id/settings`: owner or moderator,
  `{"allow_participant_screen_share": true|false}`.
- `POST /rooms/:room_id/screen-share/start`: `{"identity":"user-ID-UUID"}`;
  only a connected session of the requesting user is granted. Returns `409` if
  another session holds the slot.
- `POST /rooms/:room_id/screen-share/stop`: stops the share for that identity
  (the holder, or an owner/moderator allowed to act on them).

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

## Guest sessions

Logging in is optional. `POST /guest` (no Authorization header, no body)
creates a new guest user and answers `201 {"token": "<token>", "guest": true}`.
Send the token as `Authorization: Bearer <token>` on `/rooms` requests, exactly
like a login token: guests can create and join rooms, chat, share their screen
and hold any room role.

- Guest tokens expire after `GUEST_TOKEN_TTL` (default `720h`); the client then
  gets `401` and creates a new guest. Login tokens do not expire.
- Guests get a random `guest-…` nick that is never shown to others, have no
  password and cannot log in. Registration rejects nicks starting with
  `guest-` (`nick_reserved`).
- `POST /guest` allows 30 sessions per client IP per 10 minutes, then answers
  `429 rate_limited` with `Retry-After`. Behind a reverse proxy, set
  `TRUSTED_PROXIES` (Compose default `172.16.0.0/12`) so the real client IP is
  used; `/guest` never answers `404` or `405`, which clients read as "this
  server requires an account".
- Guests whose token expired or who were inactive for `GUEST_INACTIVE_AFTER`
  (default `336h`) are deleted hourly with their room roles, microphone locks
  and room display names. Chat messages stay (they store the sender's display
  name); a room left without an owner passes to the next user who joins.

Chat shows each sender with the name they gave when creating or joining the
room, for guests and accounts alike.

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
| `nick_reserved` | Nicks starting with `guest-` are reserved for guests |
| `rate_limited` | Too many requests from this address; retry later (429) |
| `nick_taken` | Nick already registered |
| `user_not_found` | No account with this nick |
| `wrong_password` | Password does not match |
| `auth_required` | No session token sent (401) |
| `invalid_token` | Session token unknown or expired (401 on `/rooms`) |
| `name_required` | Display name missing |
| `invalid_room_request` | Display name or `NNN-NNN-NNN` room code missing |
| `room_not_found` | Room does not exist or is not active |
| `room_not_active` | Room is closing or closed |
| `not_room_owner` | Only an owner or moderator may do this |
| `owner_required` | Only an owner may do this |
| `target_protected` | Target is an owner, or a moderator acted on by a moderator |
| `invalid_role` | Role must be `owner`, `moderator` or `participant` |
| `last_owner` | An owner may step down only while another owner is connected |
| `invalid_webhook` | LiveKit webhook signature missing or invalid (401) |
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
