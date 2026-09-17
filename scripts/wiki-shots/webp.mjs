// Converts ./shots/<png> to ../../docs/wiki/assets/shots/<name>.webp for the
// figures the wiki source names (":::shot <name>"). Edit the map when a page
// gains a figure.
import sharp from "sharp";
import { statSync } from "node:fs";

const map = {
  login: "signin", "fleet-empty": "fleet-empty", "node-add": "node-add", "node-drift": "node-agent-update",
  "node-settings": "node-port-pool", "audit-proxy": "audit-client-addresses", fleet: "fleet", deploy: "deploy",
  "deploy-bepinex": "deploy-bepinex", files: "files", sftp: "sftp", backups: "backups",
  "audit-failures": "audit-failures", specs: "specs",
};
let total = 0;
for (const [src, dst] of Object.entries(map)) {
  const out = `../../docs/wiki/assets/shots/${dst}.webp`;
  await sharp(`shots/${src}.png`).webp({ quality: 82, effort: 6 }).toFile(out);
  const size = statSync(out).size;
  total += size;
  console.log(dst.padEnd(24), `${(size / 1024).toFixed(0)}K`);
}
console.log("total", `${(total / 1024).toFixed(0)}K`);
