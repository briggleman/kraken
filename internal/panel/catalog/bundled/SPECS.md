# Kraken Game Spec authoring conventions

This directory is the bundled catalog — YAML specs `go:embed`ded into the
Panel binary. Each `*.yaml` describes one game the operator can import in
`/catalog`.

## Image assets — pick the right URL

Consistent Catalog / Specs cards mean **every spec uses the same two
derivatives**, always sourced from the **game** appid (not the dedicated-
server appid, which usually has no CDN assets).

### `banner_url` → the store **main capsule** (`capsule_616x353.jpg`)
Wide 616×353 store banner. Kraken cards crop this horizontally.
Pattern (path may include a hash directory and an `_alt_assets_N` suffix
depending on how the store owner packages assets):
```
https://shared.fastly.steamstatic.com/store_item_assets/steam/apps/<game_appid>/[<hash>/]capsule_616x353[_alt_assets_N].jpg?t=<ts>
```

### `icon_url` → the **community icon**
Small square icon used in the header + list rows. Hashed path — you can't
derive it from the appid alone.
```
https://shared.fastly.steamstatic.com/community_assets/images/apps/<game_appid>/<hash>.jpg
```

## How to find the URLs

For any game appid, this shell one-liner harvests both:

```bash
appid=427520
curl -sS "https://store.steampowered.com/app/${appid}" -A "Mozilla/5.0" \
  -H "Accept-Language: en-US" \
  -b "wants_mature_content=1;birthtime=568022401;mature_content=1" |
  grep -oE 'https://[^"'"'"']*(capsule_616x353[^"'"'"']*\.jpg|community_assets/images/apps/'"$appid"'/[a-f0-9]+\.jpg)' |
  sort -u
```

Pin the exact URLs you get (including any `?t=<timestamp>` cache-buster) into
`banner_url` / `icon_url` verbatim — the timestamp keeps the CDN version
stable across builds.

If the store page requires an age gate, the `wants_mature_content` /
`birthtime` cookies above bypass it. If the returned page has zero matches,
the game may not have public store assets (Jagex-published titles have done
this) — leave `banner_url` / `icon_url` unset and the card falls back to the
no-image style.

## Appid selection

Games that ship a **separate dedicated-server appid** (Enshrouded, V Rising,
Abiotic Factor, Windrose, Dragonwilds) have TWO Steam appids:
- **Game appid** → `banner_url` / `icon_url` (assets)
- **Dedicated-server appid** → `steam_app_ids.linux` / `.windows` (SteamCMD)

Never point the image URLs at the server appid — it has no store CDN entry.

## Related

- Spec schema: [`internal/shared/spec/spec.go`](../../../shared/spec/spec.go)
- Settings field types + config-file formats: [`internal/shared/spec/settings.go`](../../../shared/spec/settings.go)
- Catalog import loader: [`internal/panel/catalog/catalog.go`](../catalog.go)
