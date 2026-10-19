#!/usr/bin/env python3
"""Bounded pgBackRest runner for a reviewed Cerberus pipeline.

No raw SQL, arbitrary pgBackRest arguments or automatic restore. Credentials
belong in the private mounted pgBackRest configuration, never this inventory.
"""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import fcntl


def invoke(argv, timeout=300, data=None):
    result = subprocess.run(argv, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        # Tool diagnostics can include connection settings; never relay them.
        raise RuntimeError('backup operation failed; inspect private pgBackRest logs')
    return result.stdout


def load(path):
    p = Path(path)
    if p.stat().st_mode & 0o022:
        raise ValueError('backup inventory must not be group/world writable')
    config = json.loads(p.read_text())
    if set(config) != {'container', 'image_id', 'volume', 'stanza', 'max_backup_age_hours'}:
        raise ValueError('backup inventory has unexpected or missing fields')
    for field in ('container', 'volume', 'stanza'):
        if not re.fullmatch(r'[a-z][a-z0-9_-]{0,100}', config[field]):
            raise ValueError('invalid backup target identifier')
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', config['image_id']):
        raise ValueError('backup image must be pinned by image ID')
    if type(config['max_backup_age_hours']) is not int or not 1 <= config['max_backup_age_hours'] <= 48:
        raise ValueError('backup freshness limit must be 1..48 hours')
    info = json.loads(invoke(['docker', 'inspect', config['container']]))[0]
    if info['Image'] != config['image_id'] or not info['State']['Running']:
        raise ValueError('backup target is not the reviewed running image')
    mounts = [m for m in info['Mounts'] if m['Destination'] == '/var/lib/postgresql/data']
    if len(mounts) != 1 or mounts[0].get('Name') != config['volume']:
        raise ValueError('backup target data volume changed')
    return config


def run(config, operation):
    args = ['docker', 'exec', '-u', 'postgres', config['container'], 'pgbackrest',
            '--stanza=' + config['stanza'], '--log-level-console=error']
    if operation == 'full':
        invoke(args + ['--type=full', 'backup'], timeout=3600)
    elif operation == 'check':
        invoke(args + ['check'], timeout=120)
    elif operation != 'status':
        raise ValueError('unsupported backup operation')
    information = json.loads(invoke(args + ['--output=json', 'info']))
    if len(information) != 1 or information[0]['name'] != config['stanza']:
        raise ValueError('backup repository returned unexpected stanza')
    stanza = information[0]
    if stanza['status']['code'] != 0 or stanza.get('cipher') != 'aes-256-cbc' or not stanza['backup']:
        raise RuntimeError('backup repository has no healthy backup')
    latest = max(stanza['backup'], key=lambda b: b['timestamp']['stop'])
    age = dt.datetime.now(dt.timezone.utc).timestamp() - latest['timestamp']['stop']
    if age < 0 or age > config['max_backup_age_hours'] * 3600:
        raise RuntimeError('last completed backup is stale')
    health = json.loads(invoke(['docker', 'exec', '-i', '-u', 'postgres', config['container'], 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'postgres'], data=b"SELECT json_build_object('archive_mode',current_setting('archive_mode'),'pending_wal',count(*),'oldest_pending_seconds',coalesce(extract(epoch from now()-min(modification)),0)) FROM pg_ls_archive_statusdir() WHERE name LIKE '%.ready';"))
    if health['archive_mode'] != 'on' or health['oldest_pending_seconds'] > 300:
        raise RuntimeError('WAL archiving is disabled or delayed beyond five minutes')
    disk = invoke(['docker', 'exec', config['container'], 'df', '-Pk', '/var/lib/postgresql/data']).decode().splitlines()[-1].split()
    if int(disk[-2].rstrip('%')) >= 85:
        raise RuntimeError('PostgreSQL data disk is at least 85 percent full; check retained WAL')
    return {'stanza': config['stanza'], 'repository_status': 'ok',
            'latest_backup': latest['label'], 'age_seconds': round(age),
            'operation': operation, 'pending_wal': health['pending_wal'], 'off_host_restore_verified': False}


def lock(inventory):
    # The private inventory's sibling lock arbitrates manual/scheduled runs.
    fd = os.open(inventory + '.lock', os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    f = os.fdopen(fd, 'w')
    try:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except Exception:
        f.close()
        raise
    return f


def watch(inventory):
    """Cerberus-supervised worker; daily full and a five-minute health ceiling.

    Run a full backup on startup when needed, then every 24h. The repository
    remains the source of truth across worker restarts; failures retry in 15m.
    """
    while True:
        delay = 300
        try:
            with lock(inventory):
                cfg = load(inventory)
                try:
                    status = run(cfg, 'status')
                    needs_full = status['age_seconds'] >= 24*3600
                except RuntimeError as error:
                    if str(error) not in ('last completed backup is stale', 'backup repository has no healthy backup'):
                        raise
                    # A failed health check must remain visible; a backup may
                    # repair a missing/stale chain, but never clears the alert
                    # until all repository, archiver and disk checks pass.
                    needs_full = True
                result = run(cfg, 'full' if needs_full else 'check')
                print(json.dumps({'ok': True, **result}), flush=True)
        except Exception:
            print('{"ok":false,"next":"inspect backup repository, WAL archive and disk pressure"}', flush=True)
            delay = 900
        time.sleep(delay)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--inventory', required=True)
    parser.add_argument('operation', choices=['full', 'check', 'status', 'watch'])
    args = parser.parse_args()
    if args.operation == 'watch':
        return watch(args.inventory)
    try:
        if args.operation == 'status':
            result = run(load(args.inventory), args.operation)
        else:
            with lock(args.inventory):
                result = run(load(args.inventory), args.operation)
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired, KeyError, TypeError) as error:
        print(json.dumps({'ok': False, 'error': type(error).__name__,
                          'next': 'inspect the reviewed backup inventory and private pgBackRest logs'}))
        return 1
    print(json.dumps({'ok': True, **result}))
    return 0


if __name__ == '__main__':
    sys.exit(main())
