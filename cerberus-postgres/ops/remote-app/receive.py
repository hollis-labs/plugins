#!/usr/bin/env python3
"""Install one reviewed runtime credential bundle over private SSH stdin.

Install this reviewed helper on the target first. Its private target profile is
local policy: incoming data cannot choose a destination, database, user or app.
No source code or command is accepted from the delivery request.
"""
import argparse
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile


def private_read(path, limit=65536):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as f:
        st = os.fstat(f.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_mode & 0o077 or st.st_uid != os.geteuid():
            raise ValueError('private operator-owned regular file required')
        value = f.read(limit + 1)
        if len(value) > limit:
            raise ValueError('private input exceeds size bound')
        return value


def load_profile(path):
    cfg = json.loads(private_read(path))
    keys = {'app', 'host', 'port', 'database', 'user', 'reference', 'directory', 'ca_file'}
    if set(cfg) != keys or not re.fullmatch(r'[a-z][a-z0-9_-]{0,47}', cfg['app']):
        raise ValueError('invalid receiver profile')
    if not re.fullmatch(r'[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?', cfg['host']) or type(cfg['port']) is not int or not 1 <= cfg['port'] <= 65535:
        raise ValueError('reviewed DNS endpoint required')
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,62}', cfg['database']) or not re.fullmatch(r'cb_[a-z0-9_]+_r[1-9][0-9]*', cfg['user']):
        raise ValueError('runtime generation required')
    if not cfg['reference'].startswith('file:///') or not os.path.isabs(cfg['directory']) or cfg['ca_file'] and not os.path.isabs(cfg['ca_file']):
        raise ValueError('invalid receiver paths')
    directory = Path(cfg['directory'])
    if directory.is_symlink():
        raise ValueError('symlink destination refused')
    st = directory.stat()
    if not stat.S_ISDIR(st.st_mode) or st.st_mode & 0o077 or st.st_uid != os.geteuid() or str(directory.resolve()) != str(directory):
        raise ValueError('receiver directory must already exist, owned and private')
    return cfg


def receive(cfg, payload, probe=None):
    keys = {'app', 'host', 'port', 'database', 'user', 'reference', 'password', 'delivery_id'}
    if set(payload) != keys or any(payload[k] != cfg[k] for k in keys - {'password', 'delivery_id'}):
        raise ValueError('delivery does not match the local receiver policy')
    if not re.fullmatch(r'[A-Za-z0-9_-]{43}', payload['password']) or not re.fullmatch(r'[a-f0-9]{32}', payload['delivery_id']):
        raise ValueError('invalid generated credential or delivery identifier')
    dest = Path(cfg['directory']) / (payload['delivery_id'] + '.json')
    # Generation files are immutable; retrying an uncertain transfer requires
    # inspection, never an automatic replay or overwrite of an active app file.
    reservation = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(reservation, 'wb') as f:
            with tempfile.TemporaryDirectory(prefix='.probe-', dir=cfg['directory']) as tmp:
                passfile = Path(tmp) / 'pgpass'
                passfile.write_text(':'.join(str(payload[k]) for k in ('host', 'port', 'database', 'user', 'password')) + '\n')
                passfile.chmod(0o600)
                if probe is not None:
                    probe(cfg, passfile)
                else:
                    env = {'PATH': '/usr/local/bin:/usr/bin:/bin', 'PGPASSFILE': str(passfile), 'PGSSLMODE': 'verify-full', 'PGCONNECT_TIMEOUT': '5', 'PGAPPNAME': cfg['app']}
                    if cfg['ca_file']:
                        env['PGSSLROOTCERT'] = cfg['ca_file']
                    command = ['psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-h', cfg['host'], '-p', str(cfg['port']), '-U', cfg['user'], '-d', cfg['database']]
                    sql = "SELECT current_user=current_setting('role',true) OR current_setting('role',true)='none'; SELECT ssl AND version IN ('TLSv1.2','TLSv1.3') FROM pg_stat_ssl WHERE pid=pg_backend_pid(); SELECT NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) FROM pg_roles WHERE rolname=current_user;"
                    result = subprocess.run(command, input=sql.encode(), env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
                    if result.returncode or result.stdout.strip() != b't\nt\nt':
                        raise RuntimeError('native verified-TLS runtime probe failed')
            bundle = {k: payload[k] for k in ('host', 'port', 'database', 'user', 'password', 'reference')}
            bundle.update({'sslmode': 'verify-full', 'sslrootcert': cfg['ca_file'], 'application_name': cfg['app']})
            f.write(json.dumps(bundle).encode());f.flush();os.fsync(f.fileno())
        fd = os.open(cfg['directory'], os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    except Exception:
        # Leave the exclusive reservation empty on failure; it carries no secret
        # and makes an interrupted delivery visible rather than replayable.
        raise
    return {'ok': True, 'delivery_id': payload['delivery_id'], 'app': cfg['app'], 'native_tls_verified': True, 'credential_file': str(dest), 'activated': False}


def main():
    parser = argparse.ArgumentParser();parser.add_argument('--profile', required=True);args = parser.parse_args()
    try:
        cfg = load_profile(args.profile)
        raw = sys.stdin.buffer.read(65537)
        if len(raw) > 65536:
            raise ValueError('delivery too large')
        result = receive(cfg, json.loads(raw))
    except Exception:
        print('{"ok":false,"error":"delivery refused; inspect target profile and native TLS connectivity"}')
        return 1
    print(json.dumps(result));return 0


if __name__ == '__main__':
    sys.exit(main())
