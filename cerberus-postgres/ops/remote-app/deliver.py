#!/usr/bin/env python3
"""Operator pipeline step: receipt-bound runtime secret to a pinned SSH target.

Plan contains metadata only. Apply requires its exact digest and acknowledgment.
The password is read only after durable admission, then travels on SSH stdin.
No generic secret lookup, remote shell text, app activation or role retirement.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
import sys
import time
from receive import private_read


def plan(path):
    cfg = json.loads(private_read(path))
    keys = {'app', 'host', 'port', 'database', 'user', 'broker_root', 'issuance_id', 'slot', 'ssh_host', 'ssh_user', 'ssh_port', 'identity_file', 'known_hosts', 'receiver', 'receiver_profile', 'audit_directory'}
    if set(cfg) != keys:
        raise ValueError('invalid delivery profile')
    for k in ('app', 'database', 'ssh_user'):
        if not re.fullmatch(r'[a-z][a-z0-9_-]{0,62}', cfg[k]):
            raise ValueError('invalid delivery identity')
    if not re.fullmatch(r'cb_[a-z0-9_]+_r[1-9][0-9]*', cfg['user']) or not re.fullmatch(r'[a-f0-9]{32}', cfg['issuance_id']):
        raise ValueError('runtime generation and issuance ID required')
    for k in ('host', 'ssh_host'):
        if not re.fullmatch(r'[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?', cfg[k]):
            raise ValueError('reviewed DNS target required')
    for k in ('port', 'ssh_port'):
        if type(cfg[k]) is not int or not 1 <= cfg[k] <= 65535:
            raise ValueError('invalid port')
    for k in ('broker_root', 'identity_file', 'known_hosts', 'receiver', 'receiver_profile', 'audit_directory'):
        if not os.path.isabs(cfg[k]) or any(x in cfg[k] for x in ('\n', '\r', '\x00')):
            raise ValueError('absolute reviewed paths required')
    receipt = json.loads(private_read(str(Path(cfg['broker_root']) / cfg['issuance_id'] / 'completed.json')))
    if receipt['id'] != cfg['issuance_id'] or receipt['connector'] != 'postgres' or receipt['state'] != 'completed':
        raise ValueError('completed PostgreSQL issuance receipt required')
    allowed = {'provision_app': 'runtime_password', 'adopt_app': 'runtime_password', 'rotate_login': 'password'}
    if allowed.get(receipt['operation']) != cfg['slot']:
        raise ValueError('only a runtime issuance slot may be delivered')
    source = Path(cfg['broker_root']) / 'secrets' / 'issued' / cfg['issuance_id'] / cfg['slot']
    reference = 'file://' + str(source)
    if receipt['references'].get(cfg['slot']) != reference:
        raise ValueError('receipt credential reference mismatch')
    # Pin the host-key file, identity public behavior and receiver code path in
    # the plan. No password or private key bytes enter the digest or preview.
    trust = private_read(cfg['known_hosts'])
    metadata = {'profile': cfg, 'reference': reference, 'receipt_digest': hashlib.sha256(json.dumps(receipt, sort_keys=True).encode()).hexdigest(), 'host_keys_digest': hashlib.sha256(trust).hexdigest()}
    digest = hashlib.sha256(json.dumps(metadata, sort_keys=True).encode()).hexdigest()
    return cfg, source, {'digest': digest, 'app': cfg['app'], 'database': cfg['database'], 'user': cfg['user'], 'reference': reference, 'ssh_host': cfg['ssh_host'], 'activation': 'separate reviewed step'}


def record(directory, identifier, state, content):
    directory = Path(directory)
    st = directory.stat()
    if directory.is_symlink() or st.st_uid != os.geteuid() or st.st_mode & 0o077:
        raise ValueError('audit directory must already exist and be private')
    fd = os.open(directory / (identifier + '-' + state + '.json'), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump({'delivery_id': identifier, 'state': state, 'at': time.time(), **content}, f);f.flush();os.fsync(f.fileno())
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def apply(cfg, source, preview, expected, ack, transport=subprocess.run):
    if not ack or expected != preview['digest']:
        raise ValueError('reviewed plan digest and acknowledgment required')
    identifier = secrets.token_hex(16)
    record(cfg['audit_directory'], identifier, 'intent', preview)
    try:
        password = private_read(str(source), 128).decode().strip()
        if not re.fullmatch(r'[A-Za-z0-9_-]{43}', password):
            raise ValueError('invalid host-generated credential')
        payload = {k: cfg[k] for k in ('app', 'host', 'port', 'database', 'user')}
        payload.update({'reference': preview['reference'], 'password': password, 'delivery_id': identifier})
        command = ['ssh', '-F', '/dev/null', '-T', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'IdentitiesOnly=yes', '-o', 'IdentityAgent=none', '-o', 'GlobalKnownHostsFile=/dev/null', '-o', 'UserKnownHostsFile=' + cfg['known_hosts'], '-o', 'ConnectTimeout=5', '-o', 'ServerAliveInterval=5', '-o', 'ServerAliveCountMax=2', '-i', cfg['identity_file'], '-p', str(cfg['ssh_port']), cfg['ssh_user'] + '@' + cfg['ssh_host'], 'python3 ' + shlex.quote(cfg['receiver']) + ' --profile ' + shlex.quote(cfg['receiver_profile'])]
        response = transport(command, input=json.dumps(payload).encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
        if response.returncode or len(response.stdout) > 8192:
            raise RuntimeError('remote delivery refused or uncertain')
        result = json.loads(response.stdout)
        if set(result) != {'ok', 'delivery_id', 'app', 'native_tls_verified', 'credential_file', 'activated'} or result['delivery_id'] != identifier or result['app'] != cfg['app'] or result['ok'] is not True or result['native_tls_verified'] is not True or result['activated'] is not False:
            raise RuntimeError('remote delivery receipt invalid')
        # Never relay arbitrary remote output or error text into the audit/log.
        record(cfg['audit_directory'], identifier, 'completed', preview)
    except Exception:
        record(cfg['audit_directory'], identifier, 'uncertain', preview)
        raise
    return {'ok': True, 'delivery_id': identifier, 'native_tls_verified': True, 'activated': False}


def main():
    parser = argparse.ArgumentParser();parser.add_argument('--profile', required=True);parser.add_argument('--expected-plan');parser.add_argument('--ack', action='store_true');parser.add_argument('operation', choices=['plan', 'apply']);args = parser.parse_args()
    try:
        cfg, source, preview = plan(args.profile)
        result = preview if args.operation == 'plan' else apply(cfg, source, preview, args.expected_plan, args.ack)
    except Exception:
        print('{"ok":false,"error":"delivery refused or uncertain; inspect private metadata receipts before retrying"}')
        return 1
    print(json.dumps(result));return 0


if __name__ == '__main__':
    sys.exit(main())
