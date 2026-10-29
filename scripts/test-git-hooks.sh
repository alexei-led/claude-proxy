#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
git init --quiet -b fixture "$fixture"
cp "$root/go.mod" "$root/.golangci.yaml" "$fixture/"
cd "$fixture"
printf '// Package fixture tests staged checks.\npackage fixture\n\n// Value returns a dummy value.\nfunc Value() int { return 1 }\n' >fixture.go
git add go.mod .golangci.yaml fixture.go

# Unstaged damage must not affect staged checks.
printf 'not Go\n' >fixture.go
bash "$root/scripts/git-hooks/pre-commit"
if [[ "$(cat fixture.go)" != 'not Go' ]]; then
	printf 'Hook modified the worktree.\n' >&2
	exit 1
fi

printf 'package fixture\nfunc Value()int{return 1}\n' >fixture.go
git add fixture.go
if bash "$root/scripts/git-hooks/pre-commit" >hook-output 2>&1; then
	printf 'Hook accepted unformatted staged Go.\n' >&2
	exit 1
fi
grep -q 'need formatting' hook-output

git symbolic-ref HEAD refs/heads/main
if bash "$root/scripts/git-hooks/pre-commit" >hook-output 2>&1; then
	printf 'Hook allowed default-branch commit.\n' >&2
	exit 1
fi
grep -q 'default-branch commits' hook-output

printf 'refs/heads/fixture 0000000000000000000000000000000000000000 refs/heads/fixture 1111111111111111111111111111111111111111\n' |
	bash "$root/scripts/git-hooks/pre-push"
if printf 'refs/heads/main 1111111111111111111111111111111111111111 refs/heads/main 0000000000000000000000000000000000000000\n' |
	bash "$root/scripts/git-hooks/pre-push" >hook-output 2>&1; then
	printf 'Hook allowed default-branch push.\n' >&2
	exit 1
fi
grep -q 'Default-branch pushes' hook-output
# The exception must work only for a root commit and a truly empty remote.
bootstrap="$fixture/bootstrap"
remote="$fixture/remote.git"
git init --quiet -b feature/bootstrap "$bootstrap"
git init --quiet --bare "$remote"
cd "$bootstrap"
printf '/validation-stamp\n' >.gitignore
printf 'check:\n\t@printf checked > validation-stamp\n' >Makefile
git add .gitignore Makefile
git -c commit.gpgsign=false -c user.name=HookFixture -c user.email=fixture@example.invalid \
	commit --quiet -m 'Create bootstrap fixture'
sha=$(git rev-parse HEAD)
printf 'refs/heads/feature/bootstrap %s refs/heads/main 0000000000000000000000000000000000000000\n' "$sha" |
	bash "$root/scripts/git-hooks/pre-push" "$remote"
test "$(cat validation-stamp)" = checked

git push --quiet "$remote" HEAD:refs/heads/main
if printf 'refs/heads/feature/bootstrap %s refs/heads/main 0000000000000000000000000000000000000000\n' "$sha" |
	bash "$root/scripts/git-hooks/pre-push" "$remote" >"$fixture/bootstrap-output" 2>&1; then
	printf 'Hook allowed bootstrap into a nonempty remote.\n' >&2
	exit 1
fi
grep -q 'Default-branch pushes' "$fixture/bootstrap-output"
printf 'Git hook fixture checks passed; project history unchanged.\n'
