#!/bin/sh
# Starts Talaria in a container.
#
# State lives in $TALARIA_HOME (default /data), which must be a volume. Settings come from environment
# variables, so a platform that has a form for them (Dokploy, say) is all that is needed. A config file is
# optional: put its JSON in TALARIA_CONFIG_JSON and it is written to the state folder at every start, so the
# deployment's settings are always what the platform says, never what an older start left behind. Keep
# secrets out of it (name the variable that holds each one, with api_key_env and token_env).
set -eu

home="${TALARIA_HOME:-/data}"
umask 077

if ! mkdir -p "$home" 2>/dev/null || [ ! -w "$home" ]; then
	echo "talaria: cannot write to $home (running as uid $(id -u))." >&2
	echo "talaria: mount a volume there that this user may write to, for example a named volume." >&2
	exit 78
fi

if [ -n "${TALARIA_CONFIG_JSON:-}" ]; then
	cfg="${TALARIA_CONFIG:-$home/config.json}"
	printf '%s\n' "$TALARIA_CONFIG_JSON" > "$cfg"
	echo "talaria: wrote $cfg from TALARIA_CONFIG_JSON" >&2
fi

# A gateway cannot ask for the vault passphrase, so when one is given but no
# vault exists yet, make it now: the passphrase is typed twice, as init asks.
if [ -n "${TALARIA_VAULT_PASSPHRASE:-}" ] && [ ! -e "$home/vault.enc" ]; then
	echo "talaria: no vault yet, creating one from TALARIA_VAULT_PASSPHRASE" >&2
	if printf '%s\n%s\n' "$TALARIA_VAULT_PASSPHRASE" "$TALARIA_VAULT_PASSPHRASE" | tal vault init >/dev/null 2>&1; then
		echo "talaria: created $home/vault.enc" >&2
	else
		echo "talaria: could not create the vault (is the passphrase at least 12 characters?). Continuing without saved logins." >&2
	fi
fi

exec tal "$@"
