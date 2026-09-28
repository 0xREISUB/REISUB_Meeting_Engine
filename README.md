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

To inspect service output:

```sh
docker compose --env-file config/stack.env logs --tail=100 backend livekit manager
```

The panel remains available when the backend or LiveKit is stopped. Its start action creates a missing service container; restart recreates the service if it is absent. Routine restarts and container recreation retain the database volume. Do not use `docker compose down -v` unless you intentionally want to delete the database.
