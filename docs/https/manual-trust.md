---
title: "Installing the CA by hand"
description: "The per-platform trust-store commands, Firefox and Chromium's own NSS store, the WSL split-trust case, and pointing Overcast at a certificate you already have."
section: "Networking"
tags:
  - certificates
  - docs
  - https
  - tls
  - trust
  - wsl
---

# Installing the CA by hand

For an environment that blocks trust-store writes, a browser with its own store,
or a certificate you would rather bring yourself. The one-command route is
[HTTPS and HTTP/2](../https.md).

## Mint without touching the trust store

`OVERCAST_TLS=auto overcast serve` creates the CA and the leaf on first run even
if you never install anything; you then own distribution of `rootCA.pem`.
Inspect what was minted:

```bash
openssl x509 -in ~/.overcast/data/ca/cert.pem -noout -subject -enddate -ext subjectAltName
```

## Install the CA

| Store | Command |
| --- | --- |
| Windows (current user) | `certutil -user -addstore Root %USERPROFILE%\.overcast\data\ca\rootCA.pem` |
| macOS (login keychain) | `security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db ~/.overcast/data/ca/rootCA.pem` |
| Linux — Debian, Ubuntu, Alpine | `sudo cp ~/.overcast/data/ca/rootCA.pem /usr/local/share/ca-certificates/overcast-local-ca.crt && sudo update-ca-certificates` |
| Linux — Fedora, RHEL | `sudo cp ~/.overcast/data/ca/rootCA.pem /etc/pki/ca-trust/source/anchors/ && sudo update-ca-trust extract` |
| Linux — Arch (p11-kit) | `sudo cp ~/.overcast/data/ca/rootCA.pem /etc/ca-certificates/trust-source/anchors/ && sudo trust extract-compat` |

Firefox and Chromium on Linux read the **NSS user database** rather than the
system bundle, so the rows above do not reach them. With `certutil` from
`libnss3-tools`:

```bash
certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "overcast local CA" -i ~/.overcast/data/ca/rootCA.pem
```

Firefox keeps its own: use the profile directory under
`~/.mozilla/firefox/*.default*` in place of `~/.pki/nssdb`, or import through
Settings → Certificates.

On platforms with no trust-store backend (FreeBSD and other non
Windows/macOS/Linux systems), certificate minting and TLS serving work exactly
the same and only the automatic install is missing. `overcast https enable` and
`overcast trust install` report that, and the commands above are the substitute.

## WSL: two trust stores, one CA

Running the daemon inside WSL with the browser on Windows splits the trust:
`sudo overcast https enable` inside WSL installs the CA into the **Linux** store,
which curl and SDKs inside WSL use, and the Windows browser never sees it.
Install the same certificate into the Windows current-user store as well.
Windows executables are callable from a WSL shell, so one line from inside WSL
does it:

```bash
certutil.exe -user -addstore Root "$(wslpath -w ~/.overcast/data/ca/rootCA.pem)"
```

Approve the confirmation dialog. Two other spellings of the same step:

- From a Windows shell, over the UNC path:
  `certutil.exe -user -addstore Root \wsl$\<distro>\home\<user>\.overcast\data\ca\rootCA.pem`
- With a Windows `overcast.exe` installed:
  `overcast.exe https enable --endpoint https://localhost:4566` — WSL2's
  localhost forwarding carries the fetch.

WSL2's localhost forwarding then makes <https://localhost:4567> — and
`localhost.overcast.sh`, which resolves to `127.0.0.1` — work from the Windows
browser directly.

## Bring your own certificate

Skip the local CA and point Overcast at any cert/key pair (mkcert output, a
corporate-issued certificate, …):

```bash
OVERCAST_TLS_CERT=/certs/cert.pem OVERCAST_TLS_KEY=/certs/key.pem overcast serve
```

This serves both the API and the web console, exactly like `auto` mode. Put the
full chain in the cert file if a private CA issued it — the web console's backend
verifies against that file plus the system roots. `OVERCAST_TLS=auto` and
`OVERCAST_TLS_CERT`/`KEY` are mutually exclusive.

One thing you give up: auto mode mints a leaf per name during the handshake, so
host-routed URLs (`{id}.execute-api.{region}.…`, Lambda function URLs, AppSync)
are covered without being enumerable. Overcast cannot do that with a certificate
it did not issue, so your own pair needs SANs for whichever of those names you
use — see
[Which names the certificate covers](./how-it-works.md#which-names-the-certificate-covers).

## Verify

```bash
curl --cacert ~/.overcast/data/ca/rootCA.pem https://localhost:4566/_overcast/health
curl --cacert ~/.overcast/data/ca/rootCA.pem -sso /dev/null -w '%{http_version}\n' https://localhost:4567/
# → 2
```

…or open <https://localhost.overcast.sh:4567>, check the padlock, and look for
`h2` in the DevTools Network panel's Protocol column.

## Related

- [Overcast in Docker over HTTPS](./docker.md) — fetching the CA from a container with `--endpoint`
- [How the local CA works](./how-it-works.md) — what is in `<data dir>/ca`, and which names the leaf covers
- [HTTPS and HTTP/2](../https.md) — the two-command setup
- [`overcast https` and `overcast trust`](../cli/tls.md) — the command reference
