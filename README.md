# protocol

The Minecraft: Bedrock Edition protocol for Helix: every packet, the binary
reader and writer, NBT, login and (soon) connections and multi-version support.

**Status as of 2026-10-29:** in development. `auth` verifies login tokens the way BDS 1.26.60 does, with the clock corrected from Microsoft's servers; NBT compounds are written with sorted keys, as BDS writes them.

Part of [Helix](https://github.com/helix-bedrock), an open-source Minecraft: Bedrock
Edition server engine in Go.

## Packages

| Package | What it has |
|---|---|
| `protocol` (root) | Reader and writer for every protocol type (the "io" layer) |
| `packet` | Every packet, the pool, batch encoder/decoder, compression and encryption |
| `login` | Login request parsing and client data |
| `auth` | Verifies login tokens like BDS: discovery as a dedicated server, OpenID keys, corrected clock, BDS's disconnect reasons |
| `nbt` | Network and little-endian NBT |
| `conn` | Packet connections over Helix raknet and nethernet: batching, compression, encryption, hooks |
| `version` | The `Protocol` interface for older Minecraft versions, `Latest`, and `Set`. Adapters live in their own repo |

## Credits

Imported from [gophertunnel](https://github.com/HashimTheArab/gophertunnel) by **Sandertv**, using the fork by
**HashimTheArab** at commit [`80c811b61860`](https://github.com/HashimTheArab/gophertunnel/commit/80c811b61860). It is MIT licensed; the
original license is in [`licenses/gophertunnel-LICENSE`](licenses/gophertunnel-LICENSE).
Helix maintains this code from here on. See [CREDITS.md](CREDITS.md).

## License

MIT. See [LICENSE](LICENSE).
