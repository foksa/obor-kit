# Upgrading an obor project from v0.40 to v0.41

## v0.41.0 (the web page runs as its own launchd agent)

**obor web.** The machine's page runs as its own launchd agent, `obor.web`, beside each project's serve agent:
`obor serve install` installs it, and the first serve launchd starts after the upgrade installs it when it is missing.
`obor status` shows it as `web: launchd pid <n> · http://127.0.0.1:8787`. A `obor web` started by hand on that port
holds it: the agent's run logs one line naming the port and exits, and launchd retries; stop the hand-started one.

```sh
# nothing to do: the agent appears at serve's first start after the upgrade
```
