package agentpb

// This file is hand-written, not generated: scripts/genproto.sh writes only
// agent.pb.go and agent_grpc.pb.go, so it survives a regeneration.

// ContainerStateLive is the one Go definition of whether a managed container is
// live — holding memory and ports on the node — from Docker's state word
// (#385). The TypeScript twin is containerLive in web/src/lib/views.svelte.ts;
// the two must agree, and the proto comment on ManagedContainer.state states
// the same rule.
//
//   - live: running, paused, restarting
//   - not live: created, exited, dead, removing (and any word Docker adds later)
//   - empty: live. An Agent that predates the state field (0.54–0.58) only ever
//     listed running containers, and an Agent that sets containers_reported
//     always fills the state, so an empty one never describes a stopped
//     container.
//
// NodeInfo.running_servers counts live containers; the Panel's untracked and
// missing readings, the retire chip and the stopped count all read this rule.
func ContainerStateLive(state string) bool {
	switch state {
	case "", "running", "paused", "restarting":
		return true
	}
	return false
}
