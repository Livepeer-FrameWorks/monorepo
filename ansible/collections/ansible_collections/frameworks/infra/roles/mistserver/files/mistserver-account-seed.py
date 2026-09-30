"""Seed the MistServer API account into the controller config before start.

Usage: mistserver-account-seed.py CONFIG [OWNER GROUP]

MIST_API_USERNAME and MIST_API_PASSWORD are read from the environment, so the
password never appears on a command line. The account is stored the way
MistServer stores it, account.<user>.password = MD5 hex digest, and every
other key and account in the config is kept. A config that already holds the
digest is not rewritten. A missing config is created. The file is written
atomically with mode 0600; OWNER and GROUP set its ownership, otherwise the
existing file's ownership is kept.

MistServer must not be running: it writes its in-memory config back to disk
on shutdown, which would drop a change made while it runs.
"""

import grp
import hashlib
import json
import os
import pwd
import sys
import tempfile


def reconcile(data, username, password):
    digest = hashlib.md5(password.encode("utf-8")).hexdigest()  # noqa: S324 - Mist's account format
    accounts = data.get("account")
    if not isinstance(accounts, dict):
        accounts = {}
    entry = accounts.get(username)
    if not isinstance(entry, dict):
        entry = {}
    if entry.get("password") == digest:
        return False
    entry["password"] = digest
    accounts[username] = entry
    data["account"] = accounts
    return True


def main(argv):
    if len(argv) not in (2, 4):
        sys.stderr.write("usage: mistserver-account-seed.py CONFIG [OWNER GROUP]\n")
        return 2
    path = argv[1]
    username = os.environ.get("MIST_API_USERNAME", "").strip() or "frameworks"
    password = os.environ.get("MIST_API_PASSWORD", "")
    if not password:
        sys.stderr.write("mistserver-account-seed: MIST_API_PASSWORD is empty\n")
        return 1

    data = {}
    stat = None
    if os.path.exists(path):
        stat = os.stat(path)
        if stat.st_size > 0:
            with open(path, encoding="utf-8") as handle:
                data = json.load(handle)
            if not isinstance(data, dict):
                sys.stderr.write("mistserver-account-seed: %s is not a JSON object\n" % path)
                return 1

    if not reconcile(data, username, password):
        return 0

    if len(argv) == 4:
        uid = pwd.getpwnam(argv[2]).pw_uid
        gid = grp.getgrnam(argv[3]).gr_gid
    elif stat is not None:
        uid, gid = stat.st_uid, stat.st_gid
    else:
        uid, gid = os.getuid(), os.getgid()

    directory = os.path.dirname(os.path.abspath(path))
    fd, tmp_path = tempfile.mkstemp(prefix=".mistserver-account-", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(data, handle, separators=(",", ":"), ensure_ascii=False)
            handle.write("\n")
        os.chmod(tmp_path, 0o600)
        if (uid, gid) != (os.getuid(), os.getgid()):
            os.chown(tmp_path, uid, gid)
        os.replace(tmp_path, path)
    except BaseException:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)
        raise
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
