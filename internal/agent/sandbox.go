package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Sandbox runs a session in a container instead of directly on the host.
// The container sees the workdir and the run folder, both mounted at their
// host paths so every path loop hands the harness stays valid, a named
// volume as its home directory, and the allowlisted session environment
// without the host's path variables. Nothing else of the host is visible:
// no code host token, no SSH agent, no project folder.
type Sandbox struct {
	// Runtime is the container CLI. Only podman is supported.
	Runtime string
	// Image is the container image; it must contain git and the harness.
	Image string
	// Home is the named volume mounted as the home directory inside the
	// container, so logins and session transcripts survive between sessions.
	Home string
	// Mounts are extra mounts in the runtime's -v syntax
	// (host-path-or-volume:container-path[:options]).
	Mounts []string
	// Args are extra arguments for the run command, placed before the image.
	Args []string
}

// Mount is a host directory made visible in the container at the same path.
type Mount struct {
	Path     string
	ReadOnly bool
}

// SandboxHome is the home directory inside the container.
const SandboxHome = "/home/agent"

// Podman is the supported sandbox runtime.
const Podman = "podman"

// containerPrefix names sandbox containers so stragglers can be found.
const containerPrefix = "loop-"

// runArgs returns the runtime arguments that run a container for one
// session: interactive when stdin carries the prompt, the mounts, the
// forwarded environment names, then the image and the harness command.
// The container is named so a session that outlives its client can be
// removed (see kill).
func (s *Sandbox) runArgs(o Options, name string, args []string, env []string, interactive bool) (string, []string) {
	container := containerPrefix + NewUUID()[:8]
	out := []string{"run", "--rm", "--name", container}
	if interactive {
		out = append(out, "-i")
	}
	out = append(out, s.commonArgs(o.Workdir, o.RunDir, o.Mounts)...)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "" || matchesAny(k, hostPathEnv) {
			continue
		}
		// A bare name copies the value from the client's environment, so
		// secrets stay off the command line.
		out = append(out, "-e", k)
	}
	out = append(out, s.Args...)
	out = append(out, s.Image, name)
	return container, append(out, args...)
}

// commonArgs are the run arguments shared by sessions and loop join: the
// user mapping, the dropped capabilities, the mounts and the home volume.
func (s *Sandbox) commonArgs(workdir, runDir string, mounts []Mount) []string {
	out := []string{
		"--userns=keep-id", "--cap-drop=all", "--security-opt=no-new-privileges",
		"-w", workdir,
		"-v", workdir + ":" + workdir + ":Z",
	}
	if runDir != "" {
		out = append(out, "-v", runDir+":"+runDir+":Z")
	}
	for _, m := range mounts {
		opt := ":z"
		if m.ReadOnly {
			opt = ":ro,z"
		}
		out = append(out, "-v", m.Path+":"+m.Path+opt)
	}
	if s.Home != "" {
		out = append(out, "-v", s.Home+":"+SandboxHome)
	}
	out = append(out, "-e", "HOME="+SandboxHome)
	for _, m := range s.Mounts {
		out = append(out, "-v", m)
	}
	return out
}

// clientEnv is the environment of the runtime client: the session
// environment, which holds the values the -e names refer to, plus what the
// client itself needs to reach its storage or a remote machine.
func (s *Sandbox) clientEnv(session []string) []string {
	out := append([]string(nil), session...)
	have := map[string]bool{}
	for _, kv := range session {
		k, _, _ := strings.Cut(kv, "=")
		have[k] = true
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !have[k] && matchesAny(k, []string{"CONTAINERS_*", "CONTAINER_*", "PODMAN_*", "DBUS_SESSION_BUS_ADDRESS"}) {
			out = append(out, kv)
		}
	}
	return out
}

// kill removes the container. Killing the runtime client, which is what a
// cancelled context does, leaves the container running; this makes sure a
// timed-out session stops editing the workdir.
func (s *Sandbox) kill(container string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, s.Runtime, "rm", "-f", container).Run()
}

// JoinCommand is what a human types to continue a sandboxed session: the
// same container with the same mounts, interactive, with the harness
// credentials from their shell.
func (s *Sandbox) JoinCommand(r *Runner, workdir, runDir, sessionID string, mounts []Mount, passthrough []string) string {
	resume := expand(strings.Fields(r.Resume), map[string]string{"session": sessionID, "workdir": workdir})
	if len(resume) == 0 {
		resume = []string{r.Command}
	}
	args := append([]string{s.Runtime, "run", "--rm", "-it"}, s.commonArgs(workdir, runDir, mounts)...)
	for _, kv := range SessionEnv(passthrough, nil) {
		k, _, _ := strings.Cut(kv, "=")
		if matchesAny(k, hostPathEnv) || !matchesAny(k, concat(agentEnv, passthrough)) {
			continue
		}
		args = append(args, "-e", k)
	}
	args = append(args, s.Args...)
	args = append(args, s.Image)
	args = append(args, resume...)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return "cd " + shellQuote(workdir) + " && " + strings.Join(quoted, " ")
}
