<script lang="ts">
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
</script>

<div
  class="sheet"
  class:open={!!ui.open.auditLog}
  id="auditLog"
  role="dialog"
  aria-modal="true"
  aria-labelledby="auditTitle"
  style="--ox: {ui.open.auditLog?.ox ?? '50%'}; --oy: {ui.open.auditLog?.oy ?? '50%'}"
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("auditLog")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="auditTitle">audit log</h2>
    <div class="prefs-note">
      <span class="synthetic">synthetic data — mock feed</span>
    </div>
  </div>
  <div class="sheet-body audit-body">
    <div class="audit-bar">
      <input class="cfg-in audit-search" type="search" placeholder="filter by actor, verb, route or target" />
      <div class="audit-chips" role="group" aria-label="Filter entries">
        <label class="audit-chip ch-all"><input class="au-r" type="radio" name="auf" checked />all</label>
        <label class="audit-chip ch-change"><input class="au-r" type="radio" name="auf" />changes</label>
        <label class="audit-chip ch-auth"><input class="au-r" type="radio" name="auf" />sign-in</label>
        <label class="audit-chip ch-fail"><input class="au-r" type="radio" name="auf" />failures</label>
      </div>
      <span class="audit-count">1,482 entries · 16 shown</span>
    </div>

    <div class="audit-table">
      <div class="audit-head">
        <span>when</span><span>actor</span><span>action</span><span>target</span><span>source</span><span class="a-c-res">status</span>
      </div>
      <div class="audit-row auth"><span class="a-t">25 aug · 11:32:01</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">auth.login</b><em class="a-route">POST /auth/login</em></span><span class="a-tgt">ben <em>· session opened · 24h lifetime</em></span><span class="a-src">192.168.1.24</span><span class="a-res s2">200</span></div>
      <div class="audit-row auth f4"><span class="a-t">25 aug · 11:31:50</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">auth.login</b><em class="a-route">POST /auth/login</em></span><span class="a-tgt">ben <em>· wrong password (attempt 3)</em></span><span class="a-src">192.168.1.24</span><span class="a-res s4">401</span></div>
      <div class="audit-row auth f4"><span class="a-t">25 aug · 11:31:45</span><span class="a-who">bem</span><span class="a-act"><b class="a-verb">auth.login</b><em class="a-route">POST /auth/login</em></span><span class="a-tgt">bem <em>· no such account — username mistyped</em></span><span class="a-src">192.168.1.24</span><span class="a-res s4">401</span></div>
      <div class="audit-row auth f4"><span class="a-t">25 aug · 09:09:59</span><span class="a-who">admin</span><span class="a-act"><b class="a-verb">auth.login</b><em class="a-route">POST /auth/login</em></span><span class="a-tgt">admin <em>· no such account</em></span><span class="a-src">192.168.1.31</span><span class="a-res s4">401</span></div>
      <div class="audit-row change"><span class="a-t">24 aug · 16:38:29</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">server.delete</b><em class="a-route">DELETE /servers/&#123;id&#125;</em></span><span class="a-tgt">valheim-test <em>· data directory kept · 12G</em></span><span class="a-src">192.168.1.24</span><span class="a-res s2">204</span></div>
      <div class="audit-row change"><span class="a-t">24 aug · 14:19:19</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">node.token</b><em class="a-route">POST /agents/bootstrap-tokens</em></span><span class="a-tgt">behemoth <em>· enrollment token issued · 15m ttl</em></span><span class="a-src">192.168.1.24</span><span class="a-res s2">201</span></div>
      <div class="audit-row change"><span class="a-t">24 aug · 09:12:51</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">backup.start</b><em class="a-route">POST /servers/&#123;id&#125;/backups</em></span><span class="a-tgt">enshrouded <em>· queued · finished 1.2G in 41s</em></span><span class="a-src">192.168.1.24</span><span class="a-res s2">202</span></div>
      <div class="audit-row change"><span class="a-t">24 aug · 08:48:25</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">server.create</b><em class="a-route">POST /servers</em></span><span class="a-tgt">palworld-02 <em>· on behemoth · 8G · port 8212</em></span><span class="a-src">192.168.1.24</span><span class="a-res s2">201</span></div>
      <div class="audit-row change f4"><span class="a-t">24 aug · 08:47:02</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">server.create</b><em class="a-route">POST /servers</em></span><span class="a-tgt">palworld-02 <em>· port 8211 already claimed on this node</em></span><span class="a-src">192.168.1.24</span><span class="a-res s4">409</span></div>
      <div class="audit-row "><span class="a-t">23 aug · 12:54:37</span><span class="a-who">node-config:8c370f76</span><span class="a-act"><b class="a-verb">node.config</b><em class="a-route">PUT /nodes/&#123;id&#125;/config</em></span><span class="a-tgt">behemoth <em>· agent applied the pushed config</em></span><span class="a-src">10.0.0.4</span><span class="a-res s2">200</span></div>
      <div class="audit-row change f4"><span class="a-t">23 aug · 12:53:52</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">agent.update</b><em class="a-route">POST /nodes/&#123;id&#125;/agent-update</em></span><span class="a-tgt">behemoth <em>· already on 0.25.0 — nothing to do</em></span><span class="a-src">192.168.1.24</span><span class="a-res s4">409</span></div>
      <div class="audit-row f5"><span class="a-t">23 aug · 12:53:52</span><span class="a-who">node-agent-update:8c370f76</span><span class="a-act"><b class="a-verb">agent.update</b><em class="a-route">POST /nodes/&#123;id&#125;/agent-update</em></span><span class="a-tgt">behemoth <em>· agent unreachable through the tunnel</em></span><span class="a-src">10.0.0.4</span><span class="a-res s5">502</span></div>
      <div class="audit-row change"><span class="a-t">23 aug · 12:53:15</span><span class="a-who">enroll:behemoth</span><span class="a-act"><b class="a-verb">node.enroll</b><em class="a-route">POST /agents/enroll</em></span><span class="a-tgt">behemoth <em>· mtls certificate issued · debian 12</em></span><span class="a-src">10.0.0.4</span><span class="a-res s2">200</span></div>
      <div class="audit-row change"><span class="a-t">22 aug · 14:42:32</span><span class="a-who">settings</span><span class="a-act"><b class="a-verb">settings.update</b><em class="a-route">PUT /settings</em></span><span class="a-tgt">cloudflare dns <em>· api token replaced</em></span><span class="a-src">192.168.1.24</span><span class="a-res s2">200</span></div>
      <div class="audit-row change f5"><span class="a-t">22 aug · 09:07:14</span><span class="a-who">ben</span><span class="a-act"><b class="a-verb">server.power</b><em class="a-route">POST /servers/&#123;id&#125;/power</em></span><span class="a-tgt">dragonwilds <em>· agent accepted, game process never exited</em></span><span class="a-src">192.168.1.24</span><span class="a-res s5">502</span></div>
      <div class="audit-row f4"><span class="a-t">21 aug · 09:02:31</span><span class="a-who">setup-external-denied</span><span class="a-act"><b class="a-verb">setup.status</b><em class="a-route">GET /setup/status</em></span><span class="a-tgt">— <em>· first-run setup refused from a non-loopback address</em></span><span class="a-src">203.0.113.41</span><span class="a-res s4">403</span></div>
    </div>

    <div class="audit-foot">
      <span class="audit-note">written by the api and retained 90 days — entries cannot be edited or removed here. 2xx is green, 4xx violet (the caller got it wrong), 5xx magenta (we did).</span>
      <span class="audit-note">source is the address the api saw. behind a reverse proxy that is the proxy, not the client, unless the panel is trusting X-Forwarded-For — until it does, treat this column as advisory.</span>
    </div>
  </div>
</div>
