# Credits

Code in this repo that was ported from other projects, as required by the
[porting rules](https://github.com/helix-bedrock/.github/blob/main/PORTING.md).

| Helix path | Source project | Source path | Commit | License |
|---|---|---|---|---|
| `*.go` (root), `packet/`, `login/` | [gophertunnel](https://github.com/HashimTheArab/gophertunnel) by Sandertv, fork by HashimTheArab | `minecraft/protocol/` | [`80c811b61860`](https://github.com/HashimTheArab/gophertunnel/commit/80c811b61860) | MIT |
| `attribute_layer.go`, `sound.go`, `memory_category.go`, `packet/clientbound_update_sound_data.go` (1.26.60 layouts BDS's reflection data does not show: environment attribute payloads, sound data events, memory categories) | [gophertunnel](https://github.com/Sandertv/gophertunnel) by Sandertv | `minecraft/protocol/` on branch `feature/26.60` | [`28a5eec10daf`](https://github.com/Sandertv/gophertunnel/commit/28a5eec10daf) | MIT |
| `login/data.go` (`ExtraData`, `ClaimOrder`: vanilla claim order and unknown claims kept when re-encoding client data) | [gophertunnel](https://github.com/HashimTheArab/gophertunnel) by Sandertv, fork by HashimTheArab | `minecraft/protocol/login/data.go` | [`845f304ded94`](https://github.com/HashimTheArab/gophertunnel/commit/845f304ded94) | MIT |
| `nbt/` | [gophertunnel](https://github.com/HashimTheArab/gophertunnel) by Sandertv, fork by HashimTheArab | `minecraft/nbt/` | [`80c811b61860`](https://github.com/HashimTheArab/gophertunnel/commit/80c811b61860) | MIT |
| `internal/` | [gophertunnel](https://github.com/HashimTheArab/gophertunnel) by Sandertv, fork by HashimTheArab | `minecraft/internal/` | [`80c811b61860`](https://github.com/HashimTheArab/gophertunnel/commit/80c811b61860) | MIT |

| `packet/encryption.go` (CFB8, `Encryption`), legacy batch format in `packet/encoder.go` and `packet/decoder.go`, skin patch fix in `login/request.go` | [gophertunnel fork](https://github.com/shawtymarco/gophertunnel) by shawtymarco | `minecraft/protocol/` | [`9e7f28092180`](https://github.com/shawtymarco/gophertunnel/commit/9e7f28092180) | MIT |
