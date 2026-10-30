# AGENTS.md

Rules for AI coding agents working in this repo. Also read the org-wide rules in
`../.github/AGENT_RULES.md` (online:
<https://github.com/helix-bedrock/.github/blob/main/AGENT_RULES.md>). Where they
disagree, this file wins for this repo.

## What this repo is

The Minecraft: Bedrock Edition protocol for Helix: packets, the binary reader
and writer, NBT, login, and later connections and multi-version support. It does
not contain any transport; Helix raknet and nethernet carry its bytes.

## Commands

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./...
```

## Layout

- Root package: `Reader`/`Writer` and every shared protocol type (the "io" layer).
- `packet`: every packet, the pool, batch encoder/decoder, compression, encryption.
- `login`: login request parsing and client data.
- `auth`: verifies multiplayer tokens the way BDS does (discovery as a dedicated server, OpenID configuration, key set, clock corrected from Microsoft's Date header), plus nbf/iat checks BDS does not do.
- `nbt`: NBT encoding.
- `conn`: packet connections over a transport (batching, compression, encryption, hooks).
- `version`: the `Protocol` interface; `conn` converts every packet through it.
- `internal`: small shared helpers; `internal/hook` is the hook list.

## Invariants: do not break these

1. Bytes on the wire match vanilla Bedrock exactly.
2. Login uses `AuthenticationType` + `Token`; the old certificate chain is only
   accepted when explicitly enabled.
3. The original copyright line in `LICENSE` stays.
4. Never panic (see the org rules), including on malformed packets.

## Gotchas

- Imported from gophertunnel (Hashim's fork); see `CREDITS.md`.
