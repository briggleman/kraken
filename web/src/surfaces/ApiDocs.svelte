<script lang="ts">
  import { istyle } from "@/lib/istyle";
  import { ui, closeSheet } from "@/lib/state.svelte";
  import { sheetFocus } from "@/lib/sheetFocus";
</script>

<div
  class="sheet"
  class:open={!!ui.open.apiDocs}
  id="apiDocs"
  role="dialog"
  aria-modal="true"
  aria-labelledby="apiTitle"
  use:istyle={`--ox: ${ui.open.apiDocs?.ox ?? '50%'}; --oy: ${ui.open.apiDocs?.oy ?? '50%'}`}
  use:sheetFocus
>
  <div class="depth-head">
    <button class="surface-btn" onclick={() => closeSheet("apiDocs")}>
      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M 6 10 V 2 M 2 6 L 6 2 L 10 6"/></svg>
      surface
    </button>
    <h2 class="depth-title" id="apiTitle">api reference</h2>
    <div class="prefs-note">
      <span class="synthetic">generated from the openapi document</span>
    </div>
  </div>
  <div class="sheet-body api-body">
    <section class="prefs-group" aria-label="About this api">
      <div class="cfg-head"><h3 class="pane-label">the surface behind the surface</h3><span class="cfg-badge enc">bearer</span></div>
      <div class="cfg">
        <p class="cfg-desc">every screen in this console is a client of the same rest api: sessions, game specs, server lifecycle, files, backups, schedules, the node registry, agent mtls enrollment, users and the audit log. anything the ui can do, a script can do.</p>
        <div class="api-facts">
          <span><em>base</em><b>/api/v1</b></span>
          <span><em>auth</em><b>bearer token</b></span>
          <span><em>rest routes</em><b>72</b></span>
          <span><em>api version</em><b>v1</b></span>
          <span><em>panel build</em><b>0.25.0</b></span>
          <span><em>schema</em><b><a class="api-link" href="/api/v1/openapi.yaml">openapi.yaml</a></b></span>
        </div>
      </div>
    </section>

    <div class="audit-bar">
      <input class="cfg-in audit-search" type="search" placeholder="filter by path, verb or summary" />
      <div class="audit-chips" role="group" aria-label="Filter routes">
        <label class="audit-chip ac-all"><input class="au-r" type="radio" name="apif" checked />all</label>
        <label class="audit-chip ac-read"><input class="au-r" type="radio" name="apif" />reads</label>
        <label class="audit-chip ac-write"><input class="au-r" type="radio" name="apif" />writes</label>
        <label class="audit-chip ac-del"><input class="au-r" type="radio" name="apif" />destructive</label>
        <label class="audit-chip ac-pub"><input class="au-r" type="radio" name="apif" />no auth</label>
      </div>
      <span class="audit-count">74 routes · 15 groups</span>
    </div>

    <nav class="api-jump" aria-label="Jump to a group"><a href="#api-auth">auth</a><a href="#api-servers">servers</a><a href="#api-specs">game specs</a><a href="#api-nodes">nodes</a><a href="#api-files">files</a><a href="#api-backups">backups</a><a href="#api-schedules">schedules</a><a href="#api-users">users &amp; access</a><a href="#api-enrollment">agent enrollment</a><a href="#api-setup">setup &amp; catalog</a><a href="#api-settings">panel settings</a><a href="#api-dns">dns &amp; forwards</a><a href="#api-audit">audit</a><a href="#api-streams">streams</a><a href="#api-meta">meta</a></nav>

      <section class="api-sec" id="api-auth" aria-label="auth">
        <div class="api-sec-head"><h3 class="pane-label">auth</h3><span class="api-sec-n">4</span><span class="api-sec-note">a session token from login carries every other call on this page.</span></div>
        <div class="api-list">
        <details class="ep m-post w is-pub">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/auth/login</span><span class="ep-r"><span class="ep-s">log in and obtain a session token</span><span class="ep-tag pub">no auth</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· username, password</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>session token, user and role</em></p><p class="ep-line"><b class="ep-code s4">401</b> <em>no such account, or wrong password</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>public — no bearer token</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/auth/logout</span><span class="ep-r"><span class="ep-s">invalidate the current session</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>the token is dead from here on</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/auth/me</span><span class="ep-r"><span class="ep-s">the current user and role</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>username, role, permissions</em></p><p class="ep-line"><b class="ep-code s4">401</b> <em>no session</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/auth/change-password</span><span class="ep-r"><span class="ep-s">change your own password</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· current_password, new_password</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>changed — existing sessions survive</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the new password is too short</em></p><p class="ep-line"><b class="ep-code s4">401</b> <em>the current password did not match</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-servers" aria-label="servers">
        <div class="api-sec-head"><h3 class="pane-label">servers</h3><span class="api-sec-n">7</span><span class="api-sec-note">one server is one game process on one node.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers</span><span class="ep-r"><span class="ep-s">list servers</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>every server the caller may see</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers</span><span class="ep-r"><span class="ep-s">create (deploy) a server from a spec</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· spec_id, node_id, name, ports, limits</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>created — install runs on the node</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the spec or the placement is not valid</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>a port or a name is already claimed on that node</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">get a server</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required · the server uuid</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the server and its live state</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such server</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/servers/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">delete a server and its data</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>keep_data</b> <em>query · optional · leave the data directory in place</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone — world, backups and config</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such server</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/power</span><span class="ep-r"><span class="ep-s">power action (start / stop / restart / kill)</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· action: start | stop | restart | kill</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">202</b> <em>accepted — the agent is working on it</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>already in that state</em></p><p class="ep-line"><b class="ep-code s5">502</b> <em>the agent took it and the process never moved</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/settings</span><span class="ep-r"><span class="ep-s">grouped game settings + current values</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>groups, fields and the value each holds</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such server</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-put w">
          <summary class="ep-sum"><span class="ep-m">PUT</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/settings</span><span class="ep-r"><span class="ep-s">update game settings values</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· a map of field name to value</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>written — most need a restart to take</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>a value is outside what the spec allows</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-specs" aria-label="game specs">
        <div class="api-sec-head"><h3 class="pane-label">game specs</h3><span class="api-sec-n">5</span><span class="api-sec-note">the recipe for a game: image, install, startup, ports, variables.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/specs</span><span class="ep-r"><span class="ep-s">list game specs</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>every spec on this panel</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/specs</span><span class="ep-r"><span class="ep-s">create a spec (accepts a JSON or a YAML body)</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json · application/yaml</b> <em>· the whole spec document</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>created</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the document did not parse, or a required key is missing</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>that slug is taken</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/specs/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">get a spec</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the spec document</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such spec</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-put w">
          <summary class="ep-sum"><span class="ep-m">PUT</span><span class="ep-p">/specs/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">update a spec (JSON or YAML)</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json · application/yaml</b> <em>· the whole spec document</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>written — running servers keep the version they deployed with</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the document did not parse</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such spec</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/specs/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">delete a spec</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>a server was deployed from it</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-nodes" aria-label="nodes">
        <div class="api-sec-head"><h3 class="pane-label">nodes</h3><span class="api-sec-n">7</span><span class="api-sec-note">a node is a machine running the agent, reached over mTLS.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/nodes</span><span class="ep-r"><span class="ep-s">list nodes</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>every registered node</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/nodes</span><span class="ep-r"><span class="ep-s">register a node</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· name, address, total_memory, port_range</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>registered — it stays offline until an agent enrolls</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the address or the capacity is not valid</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">get a node</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the node and its last known vitals</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such node</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-patch w">
          <summary class="ep-sum"><span class="ep-m">PATCH</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">update a node's schedulable capacity</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· total_memory, game_port_range</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>written</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the new capacity is below what is already placed</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">deregister a node</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone — the certificate is revoked</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>servers are still placed on it</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em>/agent-update</span><span class="ep-r"><span class="ep-s">push this panel's embedded agent binary to the node</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">202</b> <em>accepted — the push runs in the background; poll the GET below</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>already on this version, or a push is already in flight</em></p><p class="ep-line"><b class="ep-code s5">502</b> <em>the agent is not reachable through the tunnel</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em>/agent-update</span><span class="ep-r"><span class="ep-s">progress of the most recent agent push</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>phase pushing, restarting or failed, with bytes_sent / bytes_total</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no job in this panel process — read the node&apos;s agent_version instead</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em>/info</span><span class="ep-r"><span class="ep-s">live agent info — pings the node and brings it online</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>os, cpu, memory, disk, agent version</em></p><p class="ep-line"><b class="ep-code s5">504</b> <em>the node did not answer in time</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-files" aria-label="files">
        <div class="api-sec-head"><h3 class="pane-label">files</h3><span class="api-sec-n">10</span><span class="api-sec-note">everything here is scoped to one server's data directory.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files</span><span class="ep-r"><span class="ep-s">list files under a path</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>path</b> <em>query · optional · defaults to the root</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>names, sizes and modes</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such path</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/content</span><span class="ep-r"><span class="ep-s">read a single file's contents</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>path</b> <em>query · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>utf-8 text</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such file</em></p><p class="ep-line"><b class="ep-code s4">413</b> <em>too large to read in the browser</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/raw</span><span class="ep-r"><span class="ep-s">download a single file (raw bytes)</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>path</b> <em>query · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the file as an octet stream</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such file</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/download</span><span class="ep-r"><span class="ep-s">download selected paths as a zip</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· paths[]</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>a zip stream</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/mkdir</span><span class="ep-r"><span class="ep-s">create a directory</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· path</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>created</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>something is already there</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/move</span><span class="ep-r"><span class="ep-s">move or rename a path</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· from, to</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>moved</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>the destination exists</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/copy</span><span class="ep-r"><span class="ep-s">copy a path</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· from, to</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>copied</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>the destination exists</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/write</span><span class="ep-r"><span class="ep-s">write or overwrite a file</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· path, content</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>written</em></p><p class="ep-line"><b class="ep-code s4">413</b> <em>above the write limit</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/upload</span><span class="ep-r"><span class="ep-s">upload files</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>multipart/form-data</b> <em>· path, files[]</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>stored</em></p><p class="ep-line"><b class="ep-code s4">413</b> <em>above the upload limit</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/files/delete</span><span class="ep-r"><span class="ep-s">delete paths</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· paths[]</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone — there is no trash on the node</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-backups" aria-label="backups">
        <div class="api-sec-head"><h3 class="pane-label">backups</h3><span class="api-sec-n">4</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/backups</span><span class="ep-r"><span class="ep-s">list backups</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>each with its size and when it was taken</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/backups</span><span class="ep-r"><span class="ep-s">create a backup</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">202</b> <em>queued — the agent archives the data directory</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/backups/<em>&#123;backupId&#125;</em>/restore</span><span class="ep-r"><span class="ep-s">restore a backup</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>backupId</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">202</b> <em>queued — the current data directory is replaced</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>the server is running</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/backups/<em>&#123;backupId&#125;</em></span><span class="ep-r"><span class="ep-s">delete a backup</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>backupId</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such backup</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-schedules" aria-label="schedules">
        <div class="api-sec-head"><h3 class="pane-label">schedules</h3><span class="api-sec-n">4</span><span class="api-sec-note">cron on the panel, not on the node — the agent is told when the time comes.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/schedules</span><span class="ep-r"><span class="ep-s">list a server's scheduled tasks</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>each with its cron expression and last run</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/schedules</span><span class="ep-r"><span class="ep-s">create a scheduled task</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· cron, action, enabled</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>created</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the cron expression did not parse</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-put w">
          <summary class="ep-sum"><span class="ep-m">PUT</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/schedules/<em>&#123;scheduleId&#125;</em></span><span class="ep-r"><span class="ep-s">update a scheduled task</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>scheduleId</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· cron, action, enabled</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>written</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the cron expression did not parse</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such task</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/schedules/<em>&#123;scheduleId&#125;</em></span><span class="ep-r"><span class="ep-s">delete a scheduled task</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>scheduleId</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such task</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-users" aria-label="users and access">
        <div class="api-sec-head"><h3 class="pane-label">users &amp; access</h3><span class="api-sec-n">10</span><span class="api-sec-note">no route here writes another person&#8217;s password — see the note below.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/users</span><span class="ep-r"><span class="ep-s">list users</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>username, email, role, active</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-put w">
          <summary class="ep-sum"><span class="ep-m">PUT</span><span class="ep-p">/users/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">update a user's role or active state</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· role, active</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>written</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the last owner cannot be demoted</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such user</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/users/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">remove a user</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone — what they did stays in the audit log</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the last owner cannot be removed</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/users/<em>&#123;id&#125;</em>/reset</span><span class="ep-r"><span class="ep-s">send this user a password-reset link</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">202</b> <em>a one-time link was issued</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such user</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/invites</span><span class="ep-r"><span class="ep-s">list invites that have not been redeemed</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>each with its role and expiry</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/invites</span><span class="ep-r"><span class="ep-s">issue a one-time invite link</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· email, role, expires_in</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>created — the link is shown once and never again</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>that email already has an open invite</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/invites/<em>&#123;id&#125;</em></span><span class="ep-r"><span class="ep-s">revoke an unused invite</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>the link stops working immediately</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such invite</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w is-pub">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/invites/<em>&#123;token&#125;</em>/redeem</span><span class="ep-r"><span class="ep-s">accept an invite and set your own password</span><span class="ep-tag pub">no auth</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>token</b> <em>path · required · the one-time value from the link</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· username, password</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>the account exists and is signed in</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the password is too short</em></p><p class="ep-line"><b class="ep-code s4">410</b> <em>the link was used already, or it expired</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>public — no bearer token</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/roles</span><span class="ep-r"><span class="ep-s">list roles</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>owner, operator, viewer</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/permissions</span><span class="ep-r"><span class="ep-s">list known permissions</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>every permission and the roles holding it</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-enrollment" aria-label="agent enrollment">
        <div class="api-sec-head"><h3 class="pane-label">agent enrollment</h3><span class="api-sec-n">3</span><span class="api-sec-note">how a node gets a certificate: one token, exchanged once, for a signed cert.</span></div>
        <div class="api-list">
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/agents/bootstrap-tokens</span><span class="ep-r"><span class="ep-s">issue a one-time agent bootstrap token</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>the token and its short ttl</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/agents/enroll-status</span><span class="ep-r"><span class="ep-s">poll a bootstrap token's lifecycle (pending &#8594; redeemed)</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>token</b> <em>query · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>pending, redeemed or expired</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>no such token</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w is-pub">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/agents/enroll</span><span class="ep-r"><span class="ep-s">exchange a bootstrap token + CSR for a signed certificate</span><span class="ep-tag pub">no auth</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· token, csr</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the signed certificate and the CA chain</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the CSR did not parse</em></p><p class="ep-line"><b class="ep-code s4">410</b> <em>the token was redeemed already, or it expired</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>public — no bearer token</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-setup" aria-label="setup and catalog">
        <div class="api-sec-head"><h3 class="pane-label">setup &amp; catalog</h3><span class="api-sec-n">8</span><span class="api-sec-note">first run only — gated to the loopback interface, not to a role.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/setup/status</span><span class="ep-r"><span class="ep-s">first-run onboarding progress</span><span class="ep-tag lo">loopback</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>which steps are done</em></p><p class="ep-line"><b class="ep-code s4">403</b> <em>called from a non-loopback address</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>loopback only — no role can call this from off the machine</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/setup/dismiss</span><span class="ep-r"><span class="ep-s">mark first-run onboarding as finished</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>the setup shortcut is hidden permanently</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/setup/database</span><span class="ep-r"><span class="ep-s">current datastore target</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>host, port and database — never the password</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/setup/database</span><span class="ep-r"><span class="ep-s">connect Postgres — create, migrate, persist, restart</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· host, port, database, user, password</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">202</b> <em>accepted — the panel restarts onto the new datastore</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>the panel could not connect with those details</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/setup/database/test</span><span class="ep-r"><span class="ep-s">preflight a Postgres connection</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· host, port, database, user, password</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>reachable, and whether the database exists</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>it did not connect</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/setup/local-enroll</span><span class="ep-r"><span class="ep-s">issue a bootstrap token for the co-located agent</span><span class="ep-tag lo">loopback</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>the token</em></p><p class="ep-line"><b class="ep-code s4">403</b> <em>called from a non-loopback address</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>loopback only — no role can call this from off the machine</em></p></div></div>
        </details>
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/catalog</span><span class="ep-r"><span class="ep-s">list bundled starter game specs</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>every spec shipped with this build</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/catalog/<em>&#123;id&#125;</em>/import</span><span class="ep-r"><span class="ep-s">import a bundled catalog spec</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required · the catalog entry id</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">201</b> <em>imported as a local spec you can edit</em></p><p class="ep-line"><b class="ep-code s4">409</b> <em>that slug already exists here</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-settings" aria-label="panel settings">
        <div class="api-sec-head"><h3 class="pane-label">panel settings</h3><span class="api-sec-n">4</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/settings</span><span class="ep-r"><span class="ep-s">panel-global settings status</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>what is configured — never the secrets themselves</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-put w">
          <summary class="ep-sum"><span class="ep-m">PUT</span><span class="ep-p">/settings</span><span class="ep-r"><span class="ep-s">update panel-global settings</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· cloudflare_token, unifi_*, session_lifetime</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>written</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>a value was rejected</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/settings/cloudflare/test</span><span class="ep-r"><span class="ep-s">verify the stored Cloudflare token by listing its zones</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the zones the token can reach</em></p><p class="ep-line"><b class="ep-code s4">424</b> <em>Cloudflare refused the token</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/settings/unifi/test</span><span class="ep-r"><span class="ep-s">verify the stored UniFi credentials</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the existing forwards and the WAN address</em></p><p class="ep-line"><b class="ep-code s4">424</b> <em>the controller refused or did not answer</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-dns" aria-label="dns and forwards">
        <div class="api-sec-head"><h3 class="pane-label">dns &amp; forwards</h3><span class="api-sec-n">4</span><span class="api-sec-note">both of these reach outside the panel, so both can fail for reasons that are not ours.</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/dns</span><span class="ep-r"><span class="ep-s">current DNS assignment and target for a server</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the name, the record type and where it points</em></p><p class="ep-line"><b class="ep-code s4">404</b> <em>nothing is assigned</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-put w">
          <summary class="ep-sum"><span class="ep-m">PUT</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/dns</span><span class="ep-r"><span class="ep-s">assign a DNS name — creates A/CNAME and an optional SRV</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· name, type, srv</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the records Cloudflare now holds</em></p><p class="ep-line"><b class="ep-code s4">400</b> <em>that name is not in a zone the token can reach</em></p><p class="ep-line"><b class="ep-code s4">424</b> <em>Cloudflare refused the change</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-delete w">
          <summary class="ep-sum"><span class="ep-m">DELETE</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/dns</span><span class="ep-r"><span class="ep-s">remove a server's DNS records</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">204</b> <em>gone</em></p><p class="ep-line"><b class="ep-code s4">424</b> <em>Cloudflare refused the delete</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        <details class="ep m-post w">
          <summary class="ep-sum"><span class="ep-m">POST</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/forwards/<em>&#123;portName&#125;</em></span><span class="ep-r"><span class="ep-s">open or close a UniFi port forward for a server port</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p><p class="ep-line"><b>portName</b> <em>path · required · game, query or rcon</em></p></div><div class="ep-grp"><h5>request body</h5><p class="ep-line"><b>application/json</b> <em>· enabled</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>the forward as the gateway now holds it</em></p><p class="ep-line"><b class="ep-code s4">424</b> <em>the controller refused or did not answer</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>operator and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-audit" aria-label="audit">
        <div class="api-sec-head"><h3 class="pane-label">audit</h3><span class="api-sec-n">1</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/audit</span><span class="ep-r"><span class="ep-s">list recent audit entries, newest first</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>limit</b> <em>query · optional · up to 200</em></p><p class="ep-line"><b>cursor</b> <em>query · optional</em></p><p class="ep-line"><b>actor</b> <em>query · optional</em></p><p class="ep-line"><b>from</b> <em>query · optional · an RFC 3339 timestamp</em></p><p class="ep-line"><b>to</b> <em>query · optional</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>entries plus a cursor for the next page</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>owner only</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-streams" aria-label="streams">
        <div class="api-sec-head"><h3 class="pane-label">streams</h3><span class="api-sec-n">2</span><span class="api-sec-note">not in the openapi document — see the last note below.</span></div>
        <div class="api-list">
        <details class="ep m-ws">
          <summary class="ep-sum"><span class="ep-m">WS</span><span class="ep-p">/servers/<em>&#123;id&#125;</em>/ws</span><span class="ep-r"><span class="ep-s">console output, power state and stats for one server</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">101</b> <em>switching protocols</em></p><p class="ep-line"><b class="ep-code s4">403</b> <em>the origin is not allowed</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        <details class="ep m-ws">
          <summary class="ep-sum"><span class="ep-m">WS</span><span class="ep-p">/nodes/<em>&#123;id&#125;</em>/ws</span><span class="ep-r"><span class="ep-s">node vitals: cpu, memory, disk and network</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>parameters</h5><p class="ep-line"><b>id</b> <em>path · required</em></p></div><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">101</b> <em>switching protocols</em></p><p class="ep-line"><b class="ep-code s4">403</b> <em>the origin is not allowed</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        </div>
      </section>

      <section class="api-sec" id="api-meta" aria-label="meta">
        <div class="api-sec-head"><h3 class="pane-label">meta</h3><span class="api-sec-n">1</span></div>
        <div class="api-list">
        <details class="ep m-get">
          <summary class="ep-sum"><span class="ep-m">GET</span><span class="ep-p">/version</span><span class="ep-r"><span class="ep-s">panel build version</span></span></summary>
          <div class="ep-d"><div class="ep-grp"><h5>responses</h5><p class="ep-line"><b class="ep-code s2">200</b> <em>version, commit and build date</em></p></div><div class="ep-grp"><h5>access</h5><p class="ep-line"><em>viewer and up</em></p></div></div>
        </details>
        </div>
      </section>

    <div class="audit-foot">
      <span class="audit-note">the verb is coloured by what the call can do to you: a read is unlit because nothing happens, anything that writes is lit, and delete is the only verb painted crisis — it is the only one you cannot take back. response codes follow the audit log: 2xx gold, 4xx violet (the caller got it wrong), 5xx magenta (we did).</span>
      <span class="audit-note">access is the lowest of the three roles in users &amp; access that may call the route. a viewer reads, an operator runs and repairs servers, an owner changes who and what exists.</span>
      <span class="audit-note">this page lists the shape of every route, not the full schemas — those live in openapi.yaml, which is generated from the handlers. where the two disagree, the document is right.</span>
      <span class="audit-note">no route on this page writes another person&#8217;s password. an owner may issue an invite or send a reset link; the password itself is only ever set by the person who will type it, through <b>/invites/&#123;token&#125;/redeem</b>. that is why there is no <b>POST /users</b> here.</span>
      <span class="audit-note">the two websocket routes are not in the openapi document — openapi describes a request and a response, and a stream is neither. which origins may open them is pinned by <b>KRAKEN_ALLOWED_ORIGINS</b>, shown in console settings.</span>
    </div>
  </div>
</div>
