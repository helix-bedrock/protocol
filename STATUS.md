# What protocol does

Status as of 2026-11-03: **in development**, at protocol 2225 (Minecraft 1.26.60).

## What works

- Every packet, the reader and writer for every protocol type, NBT and connections over Helix
  raknet and nethernet.
- `login` turns client data into the skin other players are sent.
- `auth` verifies login tokens the way BDS 1.26.60 does, with the clock corrected from Microsoft's
  servers.
- NBT compounds are written with sorted keys, as BDS writes them.

## What is left

- **Multi-version support:** the `version` package has the interface; the adapters for older
  versions live in their own repo.
- **The API is not stable yet.** Names and behaviour may still change between commits.
