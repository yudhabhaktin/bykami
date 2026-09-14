## What and why

<!-- One paragraph. What changes, and what problem it solves. Link the issue if
there is one. If this was planned in a chat session, paste the agreed plan here
rather than re-describing it. -->

## How it was verified

<!-- The commands actually run and what they showed. "go test -race ./... passes"
is a claim; paste the failing-then-passing detail if the change fixes a bug.
Say plainly if something was NOT verified and why. -->

- [ ] `go test -race -count=1 ./...`
- [ ] `pnpm test`
- [ ] Checked manually:

## Deploy notes

<!-- Which workflow needs dispatching after merge, if any:
     api.yml / agent.yml / deploy.yml / ansible.yml / alicloud.yml
     Say "none" if this needs no deploy. Flag anything that touches the booth
     binary in a shop, or infrastructure that costs money. -->
