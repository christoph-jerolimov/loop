package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePodman records its arguments, skips everything up to the image and
// runs the harness command that follows it, like a container would.
const fakePodman = `#!/bin/sh
printf '%s\n' "$@" > podman-argv
while [ $# -gt 0 ]; do a=$1; shift; [ "$a" = "example.test/harness:1" ] && break; done
[ $# -gt 0 ] && exec "$@"
`

func setupSandbox(t *testing.T) (string, *Sandbox) {
	t.Helper()
	dir := setupFake(t, "claude")
	if err := os.WriteFile(filepath.Join(dir, "bin", "podman"), []byte(fakePodman), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, &Sandbox{Runtime: Podman, Image: "example.test/harness:1", Home: "loop-test-home", Mounts: []string{"/opt/cache:/home/agent/.cache:ro"}, Args: []string{"--memory=1g"}}
}

func TestSandboxRunsHarnessInContainer(t *testing.T) {
	dir, sb := setupSandbox(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("GOPATH", "/host/go")
	t.Setenv("MY_APP_DB", "postgres://x")
	runDir := filepath.Join(dir, "run")
	os.MkdirAll(runDir, 0o755)
	pf := promptFile(t, runDir, "do the thing")
	res, err := runner(t, "claude").Run(context.Background(), Options{
		Workdir: dir, PromptFile: pf, Model: "m1",
		Env: map[string]string{"LOOP_RUN_ID": "r1"}, EnvPassthrough: []string{"MY_APP_*"},
		Sandbox: sb, RunDir: runDir, Mounts: []Mount{{Path: "/srv/skills/team", ReadOnly: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := read(t, filepath.Join(dir, "podman-argv"))
	lines := strings.Split(strings.TrimSpace(argv), "\n")
	if lines[0] != "run" || lines[1] != "--rm" || lines[2] != "--name" || !strings.HasPrefix(lines[3], "loop-") {
		t.Errorf("container is not named and removed:\n%s", argv)
	}
	for _, want := range []string{
		"-i", "--userns=keep-id", "--cap-drop=all", "--security-opt=no-new-privileges",
		"-w\n" + dir, "-v\n" + dir + ":" + dir + ":Z", "-v\n" + runDir + ":" + runDir + ":Z",
		"-v\n/srv/skills/team:/srv/skills/team:ro,z", "-v\nloop-test-home:/home/agent", "-e\nHOME=/home/agent",
		"-v\n/opt/cache:/home/agent/.cache:ro", "--memory=1g",
		"-e\nANTHROPIC_API_KEY\n", "-e\nMY_APP_DB\n", "-e\nLOOP_RUN_ID\n",
		"example.test/harness:1\nclaude\n-p\n", "--session-id\n" + res.SessionID, "--model\nm1",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("podman argv missing %q:\n%s", want, argv)
		}
	}
	for _, absent := range []string{"-e\nPATH\n", "-e\nHOME\n", "-e\nGOPATH\n", "-e\nGITHUB_TOKEN\n", "sk-ant", "ghp_secret"} {
		if strings.Contains(argv, absent) {
			t.Errorf("podman argv must not contain %q:\n%s", absent, argv)
		}
	}
	// The image must come after every flag and the harness after the image.
	if strings.Index(argv, "--memory=1g") > strings.Index(argv, "example.test/harness:1") {
		t.Errorf("extra args must precede the image:\n%s", argv)
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != "do the thing" {
		t.Errorf("prompt did not reach the harness on stdin: %q", got)
	}
	env := read(t, filepath.Join(dir, "env"))
	if !strings.Contains(env, "ANTHROPIC_API_KEY=sk-ant") || strings.Contains(env, "GITHUB_TOKEN=") {
		t.Errorf("client environment must carry the forwarded values and nothing else:\n%s", env)
	}
}

func TestSandboxWrapsSessionCommand(t *testing.T) {
	dir, sb := setupSandbox(t)
	if err := os.WriteFile(filepath.Join(dir, "bin", "agent"), []byte(fakeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := runner(t, "cursor").Run(context.Background(), Options{Workdir: dir, PromptFile: promptFile(t, dir, "p"), Sandbox: sb})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "chat-123" {
		t.Errorf("create-chat did not run in the container: session %q", res.SessionID)
	}
}

func TestSandboxJoinCommand(t *testing.T) {
	_, sb := setupSandbox(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("MY_APP_DB", "x")
	t.Setenv("OTHER", "x")
	got := sb.JoinCommand(runner(t, "claude"), "/w/d", "/w/.loop/runs/r1", "abc", []Mount{{Path: "/s/k", ReadOnly: true}}, []string{"MY_APP_*"})
	for _, want := range []string{
		"cd /w/d && podman run --rm -it ", "-w /w/d -v /w/d:/w/d:Z -v /w/.loop/runs/r1:/w/.loop/runs/r1:Z -v /s/k:/s/k:ro,z -v loop-test-home:/home/agent -e HOME=/home/agent",
		"-e ANTHROPIC_API_KEY", "-e MY_APP_DB", "--memory=1g example.test/harness:1 claude --resume abc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("join command missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "OTHER") || strings.Contains(got, "-e PATH") {
		t.Errorf("join command forwards too much:\n%s", got)
	}
}
