# REISUB_Meeting_Engine
It is server side of REISUB_Meeting app

## Docker deployment

The Compose stack runs the Go backend and LiveKit. The backend stores SQLite data in the persistent `reisub-meeting-db` volume.

```sh
mkdir -p config
cp config/stack.env.example config/stack.env
chmod 600 config/stack.env
```

Edit `config/stack.env` before deployment. The included `devkey` and `devsecret` values are for development only; use unique credentials before production. For the first manager start only, create a one-time bootstrap password file. The manager hashes the password into its persistent volume and removes the plaintext file after successful initialization; it is never passed through container environment variables.

```sh
umask 077
openssl rand -hex 32 | tee config/admin-init.password
chmod 600 config/admin-init.password
```

Keep the displayed password for the first panel login. It is not stored in Compose environment variables or the config file; the manager hashes it and removes the one-time file during startup.

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

Build and start the complete stack, including the manager:

```sh
docker compose --env-file config/stack.env up -d --build
docker compose --env-file config/stack.env ps
```

The Compose project name and LiveKit service name match the existing deployment so Compose can manage its current `livekit-server` container rather than creating a port-conflicting second instance. The backend listens on the configured `PORT` (8080 by default). LiveKit keeps the existing API/WebSocket and WebRTC media port mappings. The manager panel listens on host loopback at port 8090 by default; access it locally at `http://127.0.0.1:8090` or place it behind a VPN-only HTTPS reverse proxy for VPN access. Do not publish it directly to the public internet because its Docker access can control the host.

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

To inspect service output:

```sh
docker compose --env-file config/stack.env logs --tail=100 backend livekit manager
```

The panel remains available when the backend or LiveKit is stopped. Its start action creates a missing service container; restart recreates the service if it is absent. Routine restarts and container recreation retain the database volume. Do not use `docker compose down -v` unless you intentionally want to delete the database.

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
