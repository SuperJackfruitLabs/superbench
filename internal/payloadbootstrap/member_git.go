package payloadbootstrap

// gitConfig precedes every bootstrap Git argv: no optional locks, hooks,
// fsmonitor, credential helper or ext transport. The file transport is allowed
// only because each source is the daemon's own mirror, named by its fixed path.
var gitConfig = []string{"--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=always"}

// gitEnvironment is the whole environment of every bootstrap Git command.
var gitEnvironment = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TEMPLATE_DIR=/dev/null"}

// GitConfig returns a copy of the fixed options before every bootstrap argv.
func GitConfig() []string { return append([]string(nil), gitConfig...) }

// GitEnvironment returns a copy of the bootstrap Git environment.
func GitEnvironment() []string { return append([]string(nil), gitEnvironment...) }

// GitStep is one fixed bootstrap Git argv (after GitConfig) and its directory.
type GitStep struct {
	Dir  string
	Args []string
}

// MemberSteps are, in order, the only Git commands that materialize member
// from its daemon mirror into checkout, with its own admin directory.
//
// The clone copies only the mirror's local branches. A source reachable only
// from the mirror's remote-tracking refs (a branch already pushed past main)
// is not in it, so the exact source object is then fetched from the same
// mirror path: no ref is created and no other ref is copied, so the bench sees
// no branch it did not ask for and the mirror is never written. Protocol v2
// serves any object the mirror holds; Materialize has already verified that
// the source is a commit there. After that the remote URL becomes the
// registered one, which no bootstrap command ever contacts.
func MemberSteps(root, mirror, checkout, admin string, member Member) []GitStep {
	return []GitStep{
		{root, []string{"clone", "--template=", "--no-local", "--no-hardlinks", "--no-checkout", "--separate-git-dir", admin, "--", mirror, checkout}},
		{checkout, []string{"-c", "protocol.version=2", "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--recurse-submodules=no", "--refmap=", "--", mirror, member.Source}},
		{checkout, []string{"config", "--local", "remote.origin.url", member.Repository}},
		{checkout, []string{"checkout", "--no-guess", "-b", member.Branch, member.Source, "--"}},
	}
}
