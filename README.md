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
port 3478. Allow TCP 7881 and UDP 3478 through the host and provider firewalls;
the WebSocket reverse proxy alone does not carry WebRTC media.

| Port | Protocol | Purpose |
|------|----------|---------|
| 443 (NPM) | TCP | API, web app, LiveKit signaling (`wss://rtc.…`) |
| 7880 | TCP | LiveKit API/WebSocket behind NPM |
| 7881 | TCP | ICE/TCP media fallback |
| 3478 | UDP | ICE/UDP media (single mux port) |

UDP 3478 is the standard STUN/TURN port. Restrictive networks such as eduroam
let it through while blocking 7881/TCP and the former 7882/UDP, which left
clients joined (signaling over 443) but without media.

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

## Roles and permissions

Every room has one full admin, its **owner**; everyone else is a
**participant** with the permissions given to them. Both are stored per room
and user in `room_roles` and checked by the backend; clients never receive a
LiveKit room-admin grant. They follow the user ID, so reconnecting or joining
from another device keeps them. Participants' permissions and removals end
when the room is closed.

| Permission | Allows |
| --- | --- |
| `share_screen` | `POST /screen-share/start` (one sharer at a time) |
| `mute_others` | lock and unlock microphones, stop someone else's screen share |
| `request_unmute` | ask someone to turn their microphone on |
| `admit_members` | turn the waiting room on or off, let people in or turn them away, see who is waiting |
| `remove_members` | `POST .../remove`: remove someone from the meeting for good |
| `end_meeting` | `POST /close` |
| `grant_permissions` | `PUT .../permissions`: give or take any permission, this one included |

Wire names always appear in this order. The owner implicitly has all seven;
a participant has none until granted. Nobody may act on the owner (including an
owner who is away, see succession) or on themselves: such requests fail with
`403 target_protected`. A missing permission fails with
`403 permission_required`. The user who creates a room is its owner;
`Room.OwnerID` only records the creator and is not used for authorization.

Each participant's role and permissions are published as the LiveKit
participant attributes `meeting.role` (`"owner"` or `"participant"`) and
`meeting.permissions` (comma-separated wire names, empty for none, all seven for
the owner) in the join token and on every change, so clients update live.
`POST /rooms` and `POST /rooms/join` return `"role"`, `"permissions"` (a list
of wire names) and `"is_host"` (true only for the owner).

- `PUT /rooms/:room_id/participants/:identity/permissions`
  `{"permissions":["share_screen", ...]}` (owner or `grant_permissions`)
  replaces the target's whole set; unknown names are ignored. It applies even
  when the target is not connected. Returns
  `{"identity": "...", "permissions": [...]}`. Taking `share_screen` from the
  current sharer stops their share.
- `POST /rooms/:room_id/transfer-ownership` `{"identity":"..."}` (owner only,
  else `403 owner_required`): the connected target becomes owner, the caller a
  participant with all seven permissions. Returns
  `{"identity": "...", "role": "owner", "caller_role": "participant"}`.
- `POST /rooms/:room_id/leave`: the caller is leaving on purpose.
- `POST /rooms/:room_id/close` (`end_meeting`) closes the room to new joins
  and deletes its LiveKit media session. Closing can be retried safely.

Rooms also close by themselves when nobody has created, joined, been admitted
to, or been connected to them for `ROOM_IDLE_AFTER` (default `24h`). Connected
clients poll the room state, which marks the room used at most hourly; a check
runs at startup and then hourly, and keeps any room that still has someone
connected in LiveKit. A closed room's code stops working for good.

A room code is the only secret needed to join, and requests under `/rooms`
answer `404` for a wrong one. After 30 such `404` answers from one client IP
within 10 minutes, every `/rooms` request from that IP gets
`429 rate_limited` with `Retry-After` until the window ends; requests for
rooms that exist never count. Counters live in memory.
- `POST /rooms/:room_id/participants/:identity/microphone` accepts
  `{"locked":true}` to mute and lock the microphone, `{"locked":false}` to
  unlock, or `{"locked":false,"request_unmute":true}` to ask the participant to
  enable it (unlocking first if needed). Locking and unlocking need
  `mute_others`, the request needs `request_unmute`, so unlocking a locked
  microphone with a request needs both. Locks persist for later joins; remote
  unmute is not enabled. Becoming owner lifts a lock.
- `POST /rooms/:room_id/participants/:identity/remove` `{}` (`remove_members`)
  records a ban (who removed whom, when) in `room_bans`, drops the target's
  permissions, releases the screen share if they hold it, and disconnects every
  session of theirs with LiveKit `RemoveParticipant` (clients see the reason
  `PARTICIPANT_REMOVED`). It also works on users who are not connected, and is
  safe to repeat. Returns `{"identity": "...", "removed": true}`.

A removed user is refused with `403 removed_from_room` by `POST /rooms/join`
(so they get no new token and can never become acting owner) and by the chat
endpoints. A client may still hold a LiveKit token or refresh token issued
before the removal and reconnect to the media server directly; the
`participant_joined` webhook removes such a session again at once. Bans are per
user ID, never per IP address, and last until the room is closed.

The former `moderator` role is gone. At the first startup after the upgrade,
moderators became participants with `share_screen`, `mute_others`,
`request_unmute`, `admit_members` and `end_meeting` (not `remove_members`); in
rooms that had
several owners, the one connected and appointed earliest stayed owner and the
others became participants with every permission.

### Waiting room

Off by default. While it is on, `POST /rooms/join` lets in directly only the
owner (even while away), holders of `admit_members`, and users admitted to the
room before (anyone who created or joined it, or was admitted, so reconnects
and rejoins never wait). Everyone else gets
`202 {"status":"waiting","request_id":"..."}`, under the name sent with the
join; joining again while waiting returns the same `request_id`. Removed users
still get `403 removed_from_room` first. Until let in, the chat answers
`403 not_admitted`.

- `PUT /rooms/:room_id/settings` `{"waiting_room": true|false}`
  (`admit_members`). Turning it off admits everyone who is waiting.
- `GET /rooms/:room_id/waiting/:request_id` (the requester only; anyone else
  gets `404`): `{"status":"waiting"}`, `{"status":"rejected"}`, or
  `{"status":"admitted", ...}` with the fields of a join response and a token
  minted at that moment. The client polls every 2 s.
- `DELETE /rooms/:room_id/waiting/:request_id`: the requester gives up.
- `POST /rooms/:room_id/waiting/:request_id/admit` and `.../reject` `{}`
  (`admit_members`). Admitting remembers the user as admitted to the room.
  Rejecting is not a ban: they may ask again and get a new request.

A request whose requester has not polled for 30 s expires; all requests end
with the room, and removing a member drops theirs. A request that is gone
answers `404 waiting_request_not_found`.

### Owner succession

When the owner is no longer connected (after `POST /leave`, or when LiveKit
reports `participant_left`/`room_finished` through the webhook):

1. the connected participant with the most permissions (ties: granted
   earliest, then joined earliest), otherwise
2. a uniformly random connected participant becomes the **acting owner**;
3. if nobody is connected, the next user to call `POST /rooms/join` does.

An owner who was away is suspended, not demoted. When they rejoin they are
owner again and the acting owner gets back the permissions they had before. An
acting owner who leaves also steps back. A transfer of ownership is final: it
ends the away owner's right to reclaim, and they keep all seven permissions as a
participant. Succession is idempotent, so `/leave` and the webhook may both
arrive.

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

### Screen sharing

Sharing needs `share_screen` (the owner has it). Only one session per room may
share at a time.

Authenticated API:

- `GET /rooms/:room_id/state`: see below.
- `POST /rooms/:room_id/screen-share/start`: `{"identity":"user-ID-UUID"}`;
  needs `share_screen`, and only a connected session of the requesting user is
  granted. Returns `409` if another session holds the slot.
- `POST /rooms/:room_id/screen-share/stop`: stops the share for that identity
  (the holder, or anyone with `mute_others` unless the holder is the owner).

All three return the state body,
`{"screen_share_identity": string, "waiting_room": bool, "waiting": [{"request_id": "...", "name": "..."}]}`,
as do the waiting-room endpoints that change it. `waiting` lists open requests
oldest first, and only for the owner and holders of `admit_members`; everyone
else gets `[]`.

Join JWTs only allow the camera and, unless locked, the microphone, even for
users with `share_screen`: screen and screen-audio sources are granted only to
the session holding the slot, through LiveKit `UpdateParticipant`, which keeps
the one-sharer rule enforceable. Whenever that grant is revoked (`share_screen`
taken away,
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

## Server policy

`GET /policy` (no authentication, CORS like `/ping`) publishes the server's
public rules as a flat JSON object:

```json
{"guests_allowed": true, "meeting_time_limit_minutes": 0, "meeting_cooldown_minutes": 0}
```

New keys may be added later; clients ignore keys they do not know. It contains
nothing private.

| Setting (`config/stack.env`) | Default | Effect |
| --- | --- | --- |
| `GUESTS_ALLOWED` | `true` | `false` makes meetings need an account (see below). A value that is not `true`/`false` also disallows guests, and the backend logs a warning at startup. |
| `MEETING_TIME_LIMIT_MINUTES` | `0` (off) | How long rooms created from now on may last (see below). |
| `MEETING_COOLDOWN_MINUTES` | `0` (off) | How long the creator and owner of a room that reached its limit wait before creating another. |

A minutes value that is not a whole number of minutes counts as `0`, with a
warning at startup.

### Meeting time limit

A room created while `MEETING_TIME_LIMIT_MINUTES` is set gets
`ends_at = created_at + limit`; rooms created before keep what they had.
For limited rooms, `POST /rooms`, `POST /rooms/join`, the waiting room's
`admitted` answer and `GET /rooms/:room_id/state` add `"seconds_left"` (whole
seconds, never negative; clients count down from it, so device clocks do not
matter) and `"ends_at"` (RFC 3339 UTC). Both are omitted for rooms without a
limit.

At `ends_at` a timer ends the room exactly like `POST /close`: LiveKit deletes
the media room, so clients are disconnected with `ROOM_DELETED`, and the room
is marked `ended_at_limit`. Joining after `ends_at` answers `404`. At startup,
rooms already past their end are ended at once and the rest are scheduled, so
a restart neither skips nor extends a limit; a failed end is retried every
30 s.

When a room ends at its limit, its creator and whoever owned it then cannot
create a room until `ends_at + MEETING_COOLDOWN_MINUTES`:
`POST /rooms` answers `429 {"code":"meeting_cooldown","retry_after_seconds":n}`
with `Retry-After`. They may still join other people's meetings. Rooms ended
any other way (`POST /close`, idle cleanup) start no wait. The wait is per user
ID.

Changing it needs `docker compose --env-file config/stack.env up -d --no-deps backend`;
LiveKit and running meetings are not affected.

## Guest sessions

Logging in is optional unless `GUESTS_ALLOWED=false`. `POST /guest` (no Authorization header, no body)
creates a new guest user and answers `201 {"token": "<token>", "guest": true}`.
Send the token as `Authorization: Bearer <token>` on `/rooms` requests, exactly
like a login token: guests can create and join rooms, chat, share their screen
and be owner or receive any permission.

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
  (default `336h`) are deleted hourly with their room roles, bans, waiting requests, microphone locks
  and room display names. Chat messages stay (they store the sender's display
  name); a room left without an owner passes to the next user who joins.
- With `GUESTS_ALLOWED=false`, `POST /guest` and every request made with a
  guest token answer `403 guests_not_allowed`, and the client asks the user to
  log in. Account tokens work as before. Guests already in a meeting are not
  disconnected; their next API call or rejoin is refused.

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
| `guests_not_allowed` | The server requires an account: no guest sessions (403, see `GET /policy`) |
| `rate_limited` | Too many requests from this address; retry later (429) |
| `meeting_cooldown` | Your last meeting reached its time limit; wait `retry_after_seconds` before creating another (429) |
| `nick_taken` | Nick already registered |
| `user_not_found` | No account with this nick |
| `wrong_password` | Password does not match |
| `auth_required` | No session token sent (401) |
| `invalid_token` | Session token unknown or expired (401 on `/rooms`) |
| `name_required` | Display name missing |
| `invalid_room_request` | Display name or `NNN-NNN-NNN` room code missing |
| `room_not_found` | Room does not exist or is not active |
| `room_not_active` | Room is closing or closed |
| `removed_from_room` | The user was removed from this room and cannot join it again (403) |
| `waiting_request_not_found` | Waiting request unknown, someone else's, expired, cancelled, or the room ended (404) |
| `not_admitted` | The waiting room is on and the caller has not been let in yet (403) |
| `permission_required` | The caller lacks the room permission this needs (403); clients treat the former `not_room_owner` the same |
| `owner_required` | Only the owner may do this |
| `target_protected` | Target is the owner or the caller |
| `invalid_webhook` | LiveKit webhook signature missing or invalid (401) |
| `invalid_identity` | Malformed participant identity |
| `identity_not_owned` | The identity belongs to another user |
| `participant_not_found` | Identity not connected to the media room |
| `media_server_not_configured` | LiveKit credentials or URL missing |
| `media_server_error` | A LiveKit call failed; retry |
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
