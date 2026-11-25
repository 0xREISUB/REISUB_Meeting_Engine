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

### Database: PostgreSQL

In Docker the backend keeps its data in PostgreSQL 17 (the `postgres`
service, volume `reisub-meeting-pg`, reachable only from the backend). Set
`POSTGRES_PASSWORD` in `config/stack.env`, letters and digits only, as it goes
into the connection URL (`openssl rand -hex 24`). Outside Docker the backend
uses PostgreSQL when `DATABASE_URL` is set
(`postgres://user:pass@host:5432/db?sslmode=disable`;
`DATABASE_MAX_CONNECTIONS`, default 25, caps its pool) and otherwise the SQLite
file at `DB_PATH`, which is fine for development and small servers. Profile
pictures stay in the directory of `DB_PATH` (the `/data` volume) either way.

Moving a server that ran on SQLite, once, from this directory:

```sh
# 1. Back up the SQLite database and stop the backend.
docker run --rm -v reisub-meeting-db:/data:ro -v /root/backups:/b alpine:3.22 \
	cp /data/reisub.db /b/reisub-before-postgres-$(date -u +%Y%m%dT%H%M%SZ).db
docker compose --env-file config/stack.env stop backend
# 2. Start PostgreSQL and copy the data across (refuses a database that
#    already has users; ids and sessions are kept).
docker compose --env-file config/stack.env up -d --build postgres
docker compose --env-file config/stack.env run --rm --no-deps \
	--entrypoint sqlite-to-postgres backend -from /data/reisub.db
# 3. Start the backend on PostgreSQL.
docker compose --env-file config/stack.env up -d --build backend
```

`reisub.db` stays in the volume untouched apart from its own migration, so
going back is setting the backend on SQLite again. Back up PostgreSQL with
`docker compose --env-file config/stack.env exec -T postgres pg_dump -U reisub
-Fc reisub > reisub-$(date -u +%F).dump` and restore with `pg_restore`.

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

`/login` answers an unknown nick and a wrong password the same way
(`invalid_credentials`, in about the same time), so nobody learns which nicks
exist. Wrong passwords are limited: 10 per address in 15 minutes, and 20 per
nick from any addresses, after which logins get `rate_limited` (429) until the
window ends; a successful login clears the nick's count. `/register` takes 10
new accounts per address an hour.

An account session lasts `ACCOUNT_TOKEN_TTL` (default 720h, 30 days) unused:
using it moves its end on, at most once a day, so only sessions left alone
that long end. Guest sessions keep their own fixed `GUEST_TOKEN_TTL`.

Session tokens (from `/login` and `/guest`) are 256 random bits. The database
keeps only their SHA-256 (`users.token_hash`), so a copy of it holds no usable
session; a request's token is hashed and looked up. Passwords are bcrypt
hashes. Databases from before this kept tokens plain: on first start the
backend hashes them in place (sessions stay valid), drops the plain column and,
on SQLite, rewrites the file so no old token is left in it.

## Social: profiles, friends and teams

Accounts (not guests: `account_required`, 403) have a profile, friends and
teams, each with a chat. Everything below needs the session token.

| Route | Does |
| --- | --- |
| `GET /me` | The user's own profile: `id`, `nick`, `display_name` (the nick when unset), `status_message`, `presence` (`online`, `busy` or `invisible`, as chosen) |
| `PATCH /me` | Changes any of `display_name` (≤ 40, empty goes back to the nick), `status_message` (≤ 80), `presence`; answers the profile |
| `PUT /me/avatar` | Multipart field `avatar`, a JPEG or PNG of at most 8 MB: its middle square, at most 256 px, becomes the profile picture (re-encoded as JPEG, without metadata); answers the profile |
| `DELETE /me/avatar` | Removes the profile picture |
| `GET /social` | Everything the social tab shows: `profile`, `friends`, `requests` (incoming), `outgoing`, `teams` |
| `POST /friends/requests` | `{"nick"}` asks someone to be friends (201 `pending`); if they had asked first, both become friends (200 `accepted`) |
| `POST /friends/requests/:id/accept` · `/decline` | Answers a request sent to the user |
| `DELETE /friends/:user_id` | Ends a friendship, or takes back a request the user sent |
| `POST /teams` | `{"name", "color"?}` (name ≤ 40, colour `#RRGGBB`, one is picked when left out); the creator is its `owner` |
| `GET /teams/:team_id` | The team with its `members` (`user`, `role`); non-members get `team_not_found` |
| `POST /teams/:team_id/members` | `{"user_id"}`: owners and admins add one of their own friends (at most 50 members) |
| `DELETE /teams/:team_id/members/:user_id` | Leaves the team, or removes someone: admins remove members, the owner anyone. An owner who leaves hands the team to the longest-serving admin, else member; the last one out deletes it |
| `PUT /teams/:team_id/members/:user_id/role` | `{"role": "admin" \| "member"}`; owner only |
| `DELETE /teams/:team_id` | Deletes the team and its chat; owner only |
| `POST /teams/:team_id/meeting` | Answers `{"room_id", "started"}`: the team's room while it can be joined, otherwise a new one. When nobody is in it yet, an invitation goes to the team chat (once per two minutes). Join it with `POST /rooms/join` |
| `GET /chats/:kind/:id/messages` | `kind` is `friend` (a user id) or `team`; the latest 100 messages, oldest first, or with `?after=<message id>` only newer ones |
| `POST /chats/:kind/:id/messages` | `{"content"}` (1–2000 characters) |
| `POST /chats/:kind/:id/read` | `{"last_id"?}` marks the conversation read up to that message (all of it without one); it never moves backwards |
| `GET /notifications` | `?after=<message id>`: the messages others sent to any of the user's conversations since then, oldest first, at most 50, each with `kind`, `target_id` and `title` (the friend's name or the team's), and `latest_id` to ask from next. Without `after`, only `latest_id`, so old messages are not announced |
| `POST /chats/friend/:id/call` | Opens a meeting room and leaves an invitation to it in the chat; answers `{"room_id"}` |

A person in these answers (`friends[]`, `requests[].user`, team `members[].user`)
is `id`, `nick`, `display_name`, `status_message`, `avatar_url` and `presence`
as friends see it: `in_meeting` while connected to an active room's media session (from the
LiveKit webhook), `busy` or `online` while they made a request in the last 45
seconds (authenticated requests refresh this every 20 seconds), else
`offline`, with `last_seen_seconds`. Users who chose `invisible` always show
`offline`, with no last seen. Clients refresh `GET /social` every few seconds
while it is open, which also keeps the user online.

### Realtime connection

`GET /ws` upgrades to a WebSocket. The app's first message says who it is,
`{"type":"auth","token":"<session token>"}` (in a message rather than the
URL, so no log holds it); the server answers `{"type":"ready"}`. A missing
or wrong first message closes it with 4400, an unknown token with 4401, a
guest with 4403. Then the server sends, as things happen:

- `{"type":"message","message":{...}}`: a new message in one of the user's
  chats, as in `GET /notifications` (also the user's own, to their other
  devices).
- `{"type":"social"}`: something in the user's `GET /social` changed (a
  friend request, a team, someone coming online, going away or into a
  meeting, a read chat); read it again.

The app sends nothing more. The server pings every 25 seconds, so proxies
keep it open; a device too slow to take its events is closed with 1013 and
should reconnect. A user with a device connected is online to friends, and
their going away is their last seen. After connecting, an app catches up
with `GET /notifications?after=` and `GET /social`, since events sent while
it was away are not kept. Behind Nginx Proxy Manager, the proxy host needs
"Websockets Support" on.

In meetings, a participant with a profile picture carries its path in the
LiveKit attribute `meeting.avatar_url` (set in the signed join token, so it
cannot be put on by anyone else); the apps show it while the camera is off.
Profile pictures (`avatar_url`, empty without one) are served by `GET
/avatars/:name` without authentication. Each upload gets a new random name,
so they are cached for good. They are kept in `AVATAR_DIR`, by default an
`avatars` directory beside the database, so on Docker they live on the data
volume.

Friends in `GET /social` also carry `unread`, how many of their messages
the user has not read (left out when none). A team in `teams[]` has `id`,
`name`, `color`, the user's `role`, `member_count`, `online_count`, `unread`
and `meeting_live`, with its `room_id` while
someone is in its meeting. A message has `id`, `sender_id`, `sender` (display
name), `sender_avatar_url`, `content`, `created_at`, `is_mine`, and `room_id` when it invites to a
meeting (its `content` is then empty).

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
| `invalid_credentials` | Login: the nick or the password is wrong (it does not say which) |
| `user_not_found` | No account with this nick (friend requests; `/login` no longer sends it) |
| `wrong_password` | No longer sent; older servers' answer to a wrong password |
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
| `invalid_message` | Chat message empty or too long (1000 characters in rooms, 2000 between friends and in teams) |
| `account_required` | Guests cannot use profiles, friends or teams (403) |
| `invalid_profile` | Display name, status message or presence not allowed |
| `cannot_add_self` | A friend request to oneself |
| `already_friends` | Already friends (409) |
| `request_pending` | A friend request to them is already waiting (409) |
| `friend_request_not_found` | No pending request with that id sent to the user (404) |
| `not_friends` | Not friends: no chat, call or adding to a team |
| `invalid_team` | Team name empty or over 40 characters, or colour not `#RRGGBB` |
| `team_not_found` | Team unknown, or the user is not in it (404) |
| `team_role_required` | Only the team's owner or admins may do this (403) |
| `already_team_member` | That person is already in the team (409) |
| `team_full` | The team has 50 members (409) |
| `invalid_image` | Profile picture missing, or not a JPEG or PNG |
| `image_too_large` | Profile picture over 8 MB or 40 megapixels (413) |

## Development

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

CI runs the same checks plus `go mod tidy -diff` on every push and pull request.

The tests run on in-memory SQLite. To run them on PostgreSQL as well, give
them a server; each test gets a schema of its own, dropped afterwards:

```sh
docker run -d --rm --name reisub-pg-test -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:17-alpine
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```
