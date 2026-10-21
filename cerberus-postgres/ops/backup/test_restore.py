#!/usr/bin/env python3
"""Actual encrypted backup + WAL/PITR proof using only disposable fixtures.

Usage: python3 test_restore.py --image IMAGE_ID
No live DB data, mounts, passwords, host ports or networks are used.
"""
import argparse
import json
import secrets
import subprocess as sp
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    name = 'cerberus-pitr-proof-' + secrets.token_hex(6)
    label = 'cerberus.fixture=postgres-pitr'
    started = time.monotonic()

    def run(argv, data=None, timeout=120):
        r = sp.run(argv, input=data, stdout=sp.PIPE, stderr=sp.PIPE, timeout=timeout)
        if r.returncode:
            # All contents are synthetic, but retain the same no-output rule.
            raise RuntimeError('fixture command failed: ' + argv[0] + ' ' + argv[-1] + ': ' + r.stderr.decode(errors='replace')[:2000] + r.stdout.decode(errors='replace')[:2000])
        return r.stdout

    def pg(sql, restored=False):
        extra = ['-h', '/restore', '-p', '55433'] if restored else []
        return run(['docker', 'exec', '-i', '-u', 'postgres', name, 'psql', '-XqAt',
                    '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'postgres'] + extra,
                   sql.encode())

    def backrest(extra):
        return run(['docker', 'exec', '-u', 'postgres', name, 'pgbackrest',
                    '--stanza=proof', '--log-level-console=error'] + extra)

    try:
        run(['docker', 'run', '-d', '--name', name, '--label', label,
             '--network', 'none', '--memory', '768m', '--cpus', '2', '--pids-limit', '128',
             '--tmpfs', '/var/lib/postgresql/data:rw,size=256m',
             '--tmpfs', '/repo:rw,size=256m', '--tmpfs', '/restore:rw,size=256m',
             '--tmpfs', '/etc/pgbackrest:rw,size=1m',
             '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', args.image,
             'postgres', '-c', 'listen_addresses=', '-c', 'archive_mode=on',
             '-c', 'archive_command=pgbackrest --stanza=proof archive-push %p',
             '-c', 'max_wal_size=80MB', '-c', 'min_wal_size=32MB'])
        for _ in range(100):
            r = sp.run(['docker', 'exec', name, 'pg_isready', '-U', 'postgres'], capture_output=True)
            if r.returncode == 0:
                break
            time.sleep(.1)
        else:
            raise RuntimeError('fixture readiness failed')
        config = ('[proof]\npg1-path=/var/lib/postgresql/data\npg1-socket-path=/var/run/postgresql\n'
                  '[global]\nrepo1-path=/repo\nrepo1-cipher-type=aes-256-cbc\n'
                  'repo1-cipher-pass=' + secrets.token_urlsafe(48) + '\n'
                  'repo1-retention-full=2\nstart-fast=y\nprocess-max=2\n'
                  'log-level-console=error\nlog-level-file=off\narchive-timeout=30\n')
        run(['docker', 'exec', '-i', name, 'sh', '-c',
             'cat > /etc/pgbackrest/pgbackrest.conf; chown postgres /etc/pgbackrest/pgbackrest.conf /repo /restore; chmod 600 /etc/pgbackrest/pgbackrest.conf; chmod 700 /restore'], config.encode())
        backrest(['stanza-create'])
        pg('CREATE TABLE public.proof(id integer PRIMARY KEY); INSERT INTO public.proof VALUES(1);')
        backrest(['--type=full', 'backup'])
        pg("INSERT INTO public.proof VALUES(2); SELECT pg_create_restore_point('cerberus_proof_target'); INSERT INTO public.proof VALUES(3); SELECT pg_switch_wal();")
        backrest(['check'])
        backrest(['--pg1-path=/restore', '--type=name', '--target=cerberus_proof_target',
                  '--target-action=promote', '--archive-mode=off', 'restore'])
        run(['docker', 'exec', '-u', 'postgres', name, 'pg_ctl', '-D', '/restore',
             '-l', '/restore/recovery.log', '-o', '-p 55433 -k /restore -c listen_addresses=', '-w', 'start'])
        assert pg('SELECT string_agg(id::text,\',\' ORDER BY id) FROM public.proof;', True).strip() == b'1,2'
        assert pg('SELECT pg_is_in_recovery();', True).strip() == b'f'
        assert pg('SELECT string_agg(id::text,\',\' ORDER BY id) FROM public.proof;').strip() == b'1,2,3'
        metadata = json.loads(backrest(['--output=json', 'info']))[0]
        assert metadata['cipher'] == 'aes-256-cbc' and metadata['backup']
        print(json.dumps({'encrypted_backup': True, 'wal_replayed': True,
                          'pitr_excluded_later_commit': True, 'source_unchanged': True,
                          'elapsed_seconds': round(time.monotonic()-started, 2),
                          'network': 'none', 'production_mounts': False}))
    finally:
        r = sp.run(['docker', 'inspect', name], capture_output=True)
        if r.returncode == 0:
            info = json.loads(r.stdout)[0]
            assert info['Config']['Labels'].get('cerberus.fixture') == 'postgres-pitr'
            run(['docker', 'rm', '-f', name])


if __name__ == '__main__':
    main()
