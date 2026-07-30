# Kraken Game Spec authoring conventions

This directory is the bundled catalog — YAML specs `go:embed`ded into the
Panel binary. Each `*.yaml` describes one game the operator can import in
`/catalog`.

## Image assets — pick the right URL

Consistent Catalog / Specs cards mean **every spec uses the same two
derivatives**, always sourced from the **game** appid (not the dedicated-
server appid, which usually has no CDN assets).

### `banner_url` → the **library hero** (`library_hero_2x.jpg`)
Ultra-wide (~3840×1240) key art with no title text baked in — the right shape
for the full-bleed rows on `/specs`, which crop hard horizontally. Prefer
`library_hero_2x`; fall back to `library_hero` when a game has no 2x asset
(Factorio). Pattern (the hash directory differs per asset, so it can't be
derived from the capsule URL):
```
https://shared.fastly.steamstatic.com/store_item_assets/steam/apps/<game_appid>/[<hash>/]library_hero_2x.jpg?t=<ts>
```

### `icon_url` → the **community icon**
Small square icon used in the header + list rows. Hashed path — you can't
derive it from the appid alone.
```
https://shared.fastly.steamstatic.com/community_assets/images/apps/<game_appid>/<hash>.jpg
```

## How to find the URLs

Ask the store API for the asset manifest — no scraping, no age gate. It returns
every derivative plus the `asset_url_format` prefix they hang off:

```bash
appid=427520
curl -sS --get "https://api.steampowered.com/IStoreBrowseService/GetItems/v1/" \
  --data-urlencode "input_json={\"ids\":[{\"appid\":$appid}],\"context\":{\"language\":\"english\",\"country_code\":\"US\"},\"data_request\":{\"include_assets\":true}}" |
  python -c "
import sys, json
a = json.load(sys.stdin)['response']['store_items'][0]['assets']
base = 'https://shared.fastly.steamstatic.com/store_item_assets/' + a['asset_url_format']
hero = a.get('library_hero_2x') or a.get('library_hero')
print('banner_url:', base.replace('\${FILENAME}', hero))
print('icon_url:  https://shared.fastly.steamstatic.com/community_assets/images/apps/$appid/%s.jpg' % a['community_icon'])
"
```

Pin the exact URLs you get (including the `?t=<timestamp>` cache-buster) into
`banner_url` / `icon_url` verbatim — the timestamp keeps the CDN version
stable across builds. Confirm each one returns `200` (`curl -I`) before
committing.

A game with no public store entry has no `assets` block at all (Jagex-published
titles have done this) — leave `banner_url` / `icon_url` unset and the row falls
back to the no-image hatch.

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
