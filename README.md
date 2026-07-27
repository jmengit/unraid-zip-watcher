# Unraid ZIP Watcher

A tiny, low-resource container that watches one folder for ZIP files and extracts each new or changed archive. It is intended for Unraid and runs as UID `99`, GID `100` by default, matching Unraid's usual `nobody:users` ownership.

## Features

- Small Go binary with an Alpine runtime image.
- Multi-architecture image: `linux/amd64` and `linux/arm64`.
- Polls a single directory non-recursively; no database and no web server.
- Waits for a ZIP to remain unchanged for two scans before reading it.
- Extracts `example.zip` into a dedicated `OUTPUT_DIR/example/` directory (default `/output/example/`).
- Persists a size/mtime fingerprint in `/state/processed.json` so completed archives are not repeated after restart.
- Rejects absolute paths, path traversal, and symbolic links in ZIP entries.
- Extracts to a temporary directory and replaces the archive's output directory only after a successful extraction.
- Optional source deletion and optional maximum uncompressed-size limit.

## Image

```text
ghcr.io/jmengit/unraid-zip-watcher:latest
```

The image is published by GitHub Actions from this repository. The container does not need network access after it is pulled.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `WATCH_DIR` | `/watch` | Directory scanned for `.zip` files; scanning is non-recursive. |
| `OUTPUT_DIR` | `/output` | Directory containing one output subdirectory per archive. |
| `STATE_DIR` | `/state` | Directory for `processed.json`. Map this to persistent appdata. |
| `POLL_INTERVAL` | `5s` | Go duration between scans, for example `5s` or `1m`. |
| `STABLE_SCANS` | `2` | Unchanged scans required before extraction. |
| `DELETE_ZIP` | `false` | Delete the source ZIP after successful extraction. |
| `MAX_UNCOMPRESSED_BYTES` | `10737418240` | Maximum total uncompressed bytes per archive; 10 GiB by default, `0` means unlimited. |
| `MAX_ENTRY_BYTES` | `2147483648` | Maximum uncompressed bytes for one entry; 2 GiB by default, `0` means unlimited. |
| `MAX_ENTRIES` | `10000` | Maximum number of entries in one archive. |

A changed ZIP with the same filename is processed again when its size or modification time changes. Existing output for that archive is replaced only after the new archive has been completely extracted.

## Docker example

```bash
docker run -d \\
  --name unraid-zip-watcher \\
  --restart unless-stopped \\
  -e POLL_INTERVAL=5s \\
  -e STABLE_SCANS=2 \\
  -e DELETE_ZIP=false \\
  -v /mnt/user/Downloads/zip-inbox:/watch:rw \\
  -v /mnt/user/Downloads/unpacked:/output:rw \\
  -v /mnt/user/appdata/unraid-zip-watcher:/state:rw \\
  ghcr.io/jmengit/unraid-zip-watcher:latest
```

The image runs as UID 99/GID 100. Ensure the mapped host paths are writable by that identity, or override the container user in your Docker runtime if your permissions model differs. The root filesystem contains no writable application state; `/watch`, `/output`, and `/state` are the intended writable mounts.

## Unraid template

[`unraid-template.xml`](./unraid-template.xml) is a ready-to-import template. In Unraid, use **Apps → Previous Apps → Add Container** (or place the XML in your user templates directory), then review the three host paths before starting the container.

The template is provided only; this project does **not** deploy anything to Unraid.

## Development

```bash
go test ./...
go vet ./...
```

The GitHub Actions workflow runs tests and builds/publishes the multi-architecture GHCR image on pushes to `main` and version tags.

## License

MIT
